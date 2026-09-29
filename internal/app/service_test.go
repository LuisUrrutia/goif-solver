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

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"go.uber.org/zap"
)

func TestConstructionDoesNotContactConfiguredNetworksOrSources(t *testing.T) {
	redis := os.Getenv("TEST_REDIS_ADDR")
	if redis == "" {
		t.Skip("run scripts/check.sh")
	}
	c, err := config.Load("../../config/sepolia.json")
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer remote.Close()
	for i := range c.Chains {
		c.Chains[i].RPCs = []config.Endpoint{{URL: remote.URL}}
	}
	c.Chains = append(c.Chains, config.Chain{ID: 1337, RPCs: []config.Endpoint{{URL: remote.URL}}, Confirmations: 2, MaxGas: 100000, MaxFeeWei: "100"})
	c.Providers.LIFI.API = remote.URL
	c.Namespace = fmt.Sprintf("lazy-app-%d", time.Now().UnixNano())
	c.Storage.URLEnv = "TEST_APP_REDIS_URL"
	t.Setenv(c.Storage.URLEnv, "redis://"+redis)
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
	c, err := config.Load("../../config/sepolia.json")
	if err != nil {
		t.Fatal(err)
	}
	c.Development = true
	c.Storage = config.Storage{Kind: config.MemoryStorage}
	c.IntentSources = c.IntentSources[1:]
	c.QuotePublisher = ""
	// An unused provider must not even parse its URL or resolve its credentials.
	c.Providers.LIFI = &config.LIFI{API: "invalid://unused", KeyEnv: "ABSENT_LIFI_KEY"}
	service, err := New(t.Context(), c, "on-chain", false, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	service.Close()
	c.Providers.LIFI = nil
	service, err = New(t.Context(), c, "on-chain", false, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if len(service.Sources) != 1 || service.Quotes != nil {
		t.Fatal("incorrect provider selection")
	}
}
