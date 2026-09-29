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
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"
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
	c.Sources = c.Sources[:1]
	d := deployment(t, c)
	for id := uint64(900); id < 903; id++ {
		chain := d.Chains[1]
		chain.ID = id
		d.Chains = append(d.Chains, chain)
		d.Signers[0].Chains = append(d.Signers[0].Chains, id)
		route := d.Routes[0]
		route.Name = fmt.Sprintf("sepolia-network-%d", id)
		route.DestinationChain = id
		d.Routes = append(d.Routes, route)
	}
	raw, err := os.ReadFile("../lifi/testdata/pilot-order.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope lifi.Envelope
	if err = json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	messages := make([][]byte, 0, len(d.Routes))
	expected := map[string]bool{}
	for i, route := range d.Routes {
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
	for i := range d.Chains {
		d.Chains[i].RPCs = []evm.Endpoint{{URL: remote.URL}}
	}
	setDeployment(t, &c, d)
	p := c.Providers["lifi"]
	p.Routes = nil
	for _, route := range d.Routes {
		p.Routes = append(p.Routes, config.Route{Protocol: escrowprotocol.IntentKind, Name: route.Name})
	}
	c.Providers["lifi"] = p
	setLIFI(t, &c, lifiSettings{API: remote.URL, KeyEnv: "TEST_SHARED_LIFI_KEY"})
	t.Setenv("TEST_SHARED_LIFI_KEY", "")
	c.Sources[0].Settings = encodeSettings(t, streamSettings{URL: "ws" + strings.TrimPrefix(remote.URL, "http") + "/"})
	service, err := New(t.Context(), c, "shared-stream", false, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if len(d.Chains) != 5 || len(service.Sources) != 1 {
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

func TestMissingIdentifierDoesNotRequireRouteRPC(t *testing.T) {
	c, err := config.Load("../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../lifi/testdata/pilot-order.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope lifi.Envelope
	if err = json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	want := envelope.Meta.ID
	envelope.Meta.ID = ""
	providers, err := lifiProvider(c, c.Providers["lifi"], zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	source, err := providers.stream(c.Sources[0])
	if err != nil {
		t.Fatal(err)
	}

	candidate, err := source.(*lifi.Stream).Resolve(t.Context(), envelope)

	if err != nil || candidate.ID != want {
		t.Fatalf("identifier hydration requires network: %s %v", candidate.ID, err)
	}
}
