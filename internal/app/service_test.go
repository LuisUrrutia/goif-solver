package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

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
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) }))
	defer remote.Close()
	for i := range c.Chains {
		c.Chains[i].RPCs = []config.Endpoint{{URL: remote.URL}}
	}
	c.Chains = append(c.Chains, config.Chain{ID: 1337, RPCs: []config.Endpoint{{URL: remote.URL}}, Confirmations: 2, MaxGas: 100000, MaxFeeWei: "100"})
	c.OrderAPI = remote.URL
	c.Namespace = fmt.Sprintf("lazy-app-%d", time.Now().UnixNano())
	c.RedisEnv = "TEST_APP_REDIS_URL"
	t.Setenv(c.RedisEnv, "redis://"+redis)
	service, err := New(t.Context(), c, "lazy-test", false, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if calls.Load() != 0 {
		t.Fatal("constructor performed network preflight/discovery")
	}
}
