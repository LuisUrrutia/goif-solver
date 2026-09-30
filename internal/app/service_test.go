package app

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"go.uber.org/zap"
)

func TestConstructionDoesNotContactConfiguredNetworksOrSources(t *testing.T) {
	redis := os.Getenv("TEST_REDIS_ADDR")
	if redis == "" {
		t.Skip("run make test-integration")
	}
	c, err := config.Load("../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer remote.Close()
	d := deployment(t, c)
	for i := range d.Chains {
		d.Chains[i].RPCs = []evm.Endpoint{{URL: remote.URL}}
	}
	d.Chains = append(d.Chains, evm.Chain{ID: 1337, RPCs: []evm.Endpoint{{URL: remote.URL}}, Confirmations: 2, MaxGas: 100000, MaxFeeWei: "100"})
	setDeployment(t, &c, d)
	setLIFI(t, &c, lifiSettings{API: remote.URL, KeyEnv: "TEST_LIFI_KEY"})
	c.Namespace = fmt.Sprintf("lazy-app-%d", time.Now().UnixNano())
	c.Storage.URLEnv = "TEST_APP_REDIS_URL"
	t.Setenv(c.Storage.URLEnv, "redis://"+redis)
	t.Setenv(c.Storage.PrimaryRunIDEnv, os.Getenv("TEST_REDIS_RUN_ID"))
	report, err := InspectStorage(t.Context(), c)
	if err != nil || !report.Approved || report.PrimaryRunID != os.Getenv("TEST_REDIS_RUN_ID") {
		t.Fatal("primary inspection did not verify injected identity", err)
	}
	service, err := New(t.Context(), c, "lazy-test", false, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if calls.Load() != 0 {
		t.Fatal("constructor performed network preflight/discovery")
	}
}

func TestDevelopmentWithoutExternalServices(t *testing.T) {
	c, err := config.Load("../../config/development.json")
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(t.Context(), c, "development", false, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if service.Quotes != nil || len(service.Sources) != 0 {
		t.Fatal("unconfigured providers initialized")
	}
	if added, err := service.Engine.Store.Enqueue(t.Context(), "local", "payload"); err != nil || !added {
		t.Fatal("memory storage unavailable", err)
	}
	other, err := New(t.Context(), c, "independent", false, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Engine.Store.Record(t.Context(), "local"); !errors.Is(err, coordination.ErrNotFound) {
		t.Fatal("memory instances share state", err)
	}
	if _, err := New(t.Context(), c, "development", true, zap.NewNop()); err == nil {
		t.Fatal("volatile development mode allowed funded execution")
	}
}

func TestOnChainOnlyDoesNotInitializeLIFI(t *testing.T) {
	c, err := config.Load("../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	c.Development = true
	c.Storage = config.Storage{Kind: config.MemoryStorage}
	c.Sources = c.Sources[1:]
	c.Publications = nil
	// An unused provider must not even parse its URL or resolve its credentials.
	setLIFI(t, &c, lifiSettings{API: "invalid://unused", KeyEnv: "ABSENT_LIFI_KEY"})
	service, err := New(t.Context(), c, "on-chain", false, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	service.Close()
	delete(c.Providers, "lifi")
	service, err = New(t.Context(), c, "on-chain", false, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if len(service.Sources) != 1 || service.Quotes != nil {
		t.Fatal("incorrect provider selection")
	}
}

func TestUnusedSettlementDoesNotResolveCredentialsOrConstructClient(t *testing.T) {
	c, err := config.Load("../../config/development.json")
	if err != nil {
		t.Fatal(err)
	}
	c.Settlements["unused"] = config.Definition{Kind: polymerKind, Settings: encodeSettings(t, polymerSettings{API: "invalid://unused", KeyEnv: "UNUSED_POLYMER_KEY", RequestMethod: "request", QueryMethod: "query"})}
	t.Setenv("UNUSED_POLYMER_KEY", "")
	if err = c.Validate(); err != nil {
		t.Fatal(err)
	}
	backends, err := configureSettlements(c, escrowprotocol.Deployment{}, nil, nil, true)
	if err != nil || len(backends) != 0 {
		t.Fatal("unused backend was initialized", err)
	}
	service, err := New(t.Context(), c, "isolated", false, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
}
