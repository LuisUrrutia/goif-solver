package app

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/ethereum/go-ethereum/ethclient"
)

func TestQuoteUsesConfiguredAssetsInventoryAndReserve(t *testing.T) {
	c, err := config.Load("../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	d := deployment(t, c)
	route := &d.Routes[0]
	route.InputDecimals, route.OutputDecimals = 18, 18
	route.MaxInput, route.MaxOutput, route.Pricing.MinMargin = "1000000000000000000", "1000000000000000000", "10000000000000000"
	var balance, calls atomic.Int64
	balance.Store(990000000000000000)
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     uint64 `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Method != "eth_call" {
			t.Error("unexpected RPC method", request.Method)
		}
		calls.Add(1)
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":"0x%064x"}`, request.ID, big.NewInt(balance.Load()))
	}))
	defer remote.Close()
	client, err := ethclient.DialContext(t.Context(), remote.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	sources, err := configureQuoteSources(d, map[uint64]*ethclient.Client{route.DestinationChain: client})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 || sources[route.Name] == nil {
		t.Fatal("route binding lost")
	}

	offer, err := sources[route.Name].Offer(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if offer.Input.Decimals != 18 || offer.Output.Decimals != 18 || offer.Input.Address != route.InputToken.Hex() || offer.Output.Address != route.OutputToken.Hex() {
		t.Fatal("configured asset policy lost")
	}
	if len(offer.Ranges) != 1 || offer.Ranges[0].Rate != "0.99" || offer.Ranges[0].Minimum != route.MaxInput {
		t.Fatalf("reserve changed: %+v", offer.Ranges)
	}
	balance.Store(989999999999999999)
	depleted, err := sources[route.Name].Offer(t.Context(), false)
	if err != nil || len(depleted.Ranges) != 0 {
		t.Fatal("advertised depleted inventory", err)
	}
	before := calls.Load()
	withdrawn, err := sources[route.Name].Offer(t.Context(), true)
	if err != nil || len(withdrawn.Ranges) != 0 || withdrawn.Input != offer.Input || withdrawn.Output != offer.Output || calls.Load() != before {
		t.Fatal("withdrawal changed identity or queried inventory", err)
	}
	remote.Close()
	if _, err = sources[route.Name].Offer(t.Context(), false); err == nil {
		t.Fatal("published despite inventory failure")
	}
}
