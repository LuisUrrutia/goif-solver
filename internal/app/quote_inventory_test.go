package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/oif"
	oifescrow "github.com/LuisUrrutia/goif-solver/internal/oif/escrow"
	protocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/quote"
	"github.com/ethereum/go-ethereum/ethclient"
)

func TestQuoteInventoryCoversPricedStandingAndRequestedOutputs(t *testing.T) {
	c, err := config.Load("../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	d := deployment(t, c)
	var balance atomic.Int64
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
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":"0x%064x"}`, request.ID, balance.Load())
	}))
	t.Cleanup(remote.Close)
	client, err := ethclient.DialContext(t.Context(), remote.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	for _, test := range []struct {
		name          string
		pricing       quote.PricingSettings
		output, small int64
	}{
		{name: "reserve", pricing: quote.PricingSettings{Kind: quote.ReservePricing, MinMargin: "10000"}, output: 990000, small: 490000},
		{name: "rate", pricing: quote.PricingSettings{Kind: quote.RatePricing, Rate: "0.5"}, output: 500000, small: 250000},
	} {
		t.Run(test.name, func(t *testing.T) {
			d.Routes[0].Pricing = test.pricing
			route := d.Routes[0]
			sources, err := configureQuoteSources(d, map[uint64]*ethclient.Client{route.DestinationChain: client})
			if err != nil {
				t.Fatal(err)
			}
			source := sources[route.Name]
			adapter := &oifescrow.Route{Policy: route, Signer: d.Signers[0].Address, Inventory: source, Verify: func(context.Context, protocol.Route) error { return nil }}
			user := d.Signers[0].Address.Hex()
			query := oif.QuoteRequest{User: oif.Address{Chain: "eip155:11155111", Address: user}, SupportedTypes: []oif.OrderType{oif.UserOpen}, Intent: oif.Swap{IntentType: oif.SwapIntent, Inputs: []oif.Input{{Chain: "eip155:11155111", User: user, Asset: route.InputToken.Hex(), Amount: "1000000"}}, Outputs: []oif.Output{{Chain: "eip155:84532", Receiver: user, Asset: route.OutputToken.Hex()}}}}
			for _, test := range []struct {
				name, input         string
				balance, output     int64
				standing, requested bool
			}{
				{name: "exact maximum obligation", input: "1000000", balance: test.output, output: test.output, standing: true, requested: true},
				{name: "below maximum obligation", input: "1000000", balance: test.output - 1},
				{name: "exact smaller obligation", input: "500000", balance: test.small, output: test.small, requested: true},
				{name: "below smaller obligation", input: "500000", balance: test.small - 1},
			} {
				t.Run(test.name, func(t *testing.T) {
					balance.Store(test.balance)
					query.Intent.Inputs[0].Amount = test.input

					standing, err := source.Offer(t.Context(), false)
					if err != nil {
						t.Fatal(err)
					}
					requested, err := adapter.Quote(t.Context(), query)

					if (len(standing.Ranges) > 0) != test.standing {
						t.Fatalf("standing inventory mismatch: %+v", standing)
					}
					if (err == nil) != test.requested {
						t.Fatalf("requested inventory mismatch: %v", err)
					}
					if test.requested && requested.Preview.Outputs[0].Amount != fmt.Sprint(test.output) {
						t.Fatal("wrong output", requested.Preview)
					}
				})
			}
		})
	}
}
