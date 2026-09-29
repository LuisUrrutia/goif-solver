package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

func TestFiveNetworksShareOneLIFIConnection(t *testing.T) {
	c, err := config.Load("../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	c.Development = true
	c.RequestsPerSecond = 100
	c.Storage = config.Storage{Kind: config.MemoryStorage}
	c.IntentSources = c.IntentSources[:1]
	for id := uint64(900); id < 903; id++ {
		chain := c.Chains[1]
		chain.ID = id
		c.Chains = append(c.Chains, chain)
		c.Signers[0].Chains = append(c.Signers[0].Chains, id)
		route := c.Routes[0]
		route.Name = fmt.Sprintf("sepolia-network-%d", id)
		route.DestinationChain = id
		c.Routes = append(c.Routes, route)
	}
	raw, err := os.ReadFile("../lifi/testdata/pilot-order.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope lifi.Envelope
	if err = json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	messages := make([][]byte, 0, len(c.Routes))
	expected := map[string]bool{}
	for i, route := range c.Routes {
		envelope.Meta.ID = fmt.Sprintf("0x%064x", i+1)
		envelope.Order.Outputs[0].ChainID = strconv.FormatUint(route.DestinationChain, 10)
		expected[envelope.Meta.ID] = true
		message, err := json.Marshal(struct {
			Event string        `json:"event"`
			Data  lifi.Envelope `json:"data"`
		}{Event: "user:vm-order-submit", Data: envelope})
		if err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
	}
	var connections atomic.Int64
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/orders" {
			_, _ = w.Write([]byte(`{"data":[],"meta":{"total":0,"limit":50,"offset":0}}`))
			return
		}
		if r.URL.Path != "/" {
			t.Error("unexpected network request", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		upgrader := websocket.Upgrader{}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		connections.Add(1)
		for _, message := range messages {
			if err = conn.WriteMessage(websocket.TextMessage, message); err != nil {
				t.Error(err)
				return
			}
		}
		for {
			if _, _, err = conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer remote.Close()
	for i := range c.Chains {
		c.Chains[i].RPCs = []config.Endpoint{{URL: remote.URL}}
	}
	c.Providers.LIFI = &config.LIFI{API: remote.URL, KeyEnv: "TEST_SHARED_LIFI_KEY"}
	t.Setenv(c.Providers.LIFI.KeyEnv, "")
	c.IntentSources[0].URL = "ws" + strings.TrimPrefix(remote.URL, "http") + "/"
	service, err := New(t.Context(), c, "shared-stream", false, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if len(c.Chains) != 5 || len(service.Sources) != 1 {
		t.Fatal("networks multiplied intent sources")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	received := map[string]bool{}

	err = service.Sources[0].Run(ctx, func(_ context.Context, candidate intent.Candidate) error {
		if !expected[candidate.ID] {
			t.Error("unexpected intent", candidate.ID)
		}
		received[candidate.ID] = true
		if len(received) == len(expected) {
			cancel()
		}
		return nil
	})

	if !errors.Is(ctx.Err(), context.Canceled) || len(received) != len(expected) || connections.Load() != 1 {
		t.Fatal("routes did not share one live feed", len(received), connections.Load(), err)
	}
}
