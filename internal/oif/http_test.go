package oif_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	workflow "github.com/LuisUrrutia/goif-solver/internal/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/oif"
	oifescrow "github.com/LuisUrrutia/goif-solver/internal/oif/escrow"
	protocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/quote"
	"github.com/LuisUrrutia/goif-solver/internal/storage/memorystore"
)

const (
	accessToken = "synthetic-oif-access-token-32-characters"
	quoteKey    = "synthetic-oif-quote-signing-key-32-characters"
)

type inventory struct{}

func (inventory) Offer(context.Context, bool) (quote.Offer, error) {
	return quote.Offer{Ranges: []quote.PriceRange{{Minimum: "1", Maximum: "1000000", Rate: "0.99"}}}, nil
}

func setup(t *testing.T) (*oif.Handler, oif.QuoteRequest, *memorystore.Store) {
	t.Helper()
	c, err := config.Load("../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	d, err := config.Decode[protocol.Deployment](c.Executions[protocol.IntentKind])
	if err != nil {
		t.Fatal(err)
	}
	store := memorystore.New()
	engine := &workflow.Engine{Config: workflow.Policy{Deployment: d, Version: c.Version}}
	route := &oifescrow.Route{Policy: d.Routes[0], Signer: d.Signers[0].Address, Gas: d.Chains[0].MaxGas, Source: inventory{}, Verify: func(context.Context, protocol.Route) error { return nil }}
	handler := &oif.Handler{Store: store, Prepare: engine.Prepare, Routes: []oif.Route{route}, Token: accessToken, QuoteKey: []byte(quoteKey), Provider: "test-solver", Node: "oif-test", RequestsPerSecond: 1000, Enabled: true}
	request := oif.QuoteRequest{User: oif.Address{Chain: "eip155:11155111", Address: "0x3333333351e46fE70247b7082Ae505c85daBEc7c"}, SupportedTypes: []oif.OrderType{oif.UserOpen}, Intent: oif.Swap{IntentType: oif.SwapIntent, Inputs: []oif.Input{{Chain: "eip155:11155111", User: "0x3333333351e46fE70247b7082Ae505c85daBEc7c", Asset: d.Routes[0].InputToken.Hex(), Amount: "1000000"}}, Outputs: []oif.Output{{Chain: "eip155:84532", Receiver: "0x3333333351e46fE70247b7082Ae505c85daBEc7c", Asset: d.Routes[0].OutputToken.Hex()}}}}
	return handler, request, store
}

func request(t *testing.T, handler http.Handler, method, path string, value any, token string) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	var err error
	if value != nil {
		raw, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func readJSON[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var result T
	if response.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestUserOpenQuoteSubmissionReplayAndDurableStatus(t *testing.T) {
	handler, query, store := setup(t)
	validateSchema(t, "GetQuoteRequest", query)
	quotes := request(t, handler.HTTP(), http.MethodPost, "/v1/quotes", query, accessToken)
	response := readJSON[oif.QuoteResponse](t, quotes)
	validateRawSchema(t, "GetQuoteResponse", quotes.Body.Bytes())
	q := response.Quotes[0]
	if q.Order.Type != oif.UserOpen || q.Preview.Outputs[0].Amount != "990000" || q.QuoteID == "" || q.ValidUntil <= time.Now().Unix() {
		t.Fatal(q)
	}
	submission := oif.Submission{Order: q.Order, QuoteID: q.QuoteID}
	validateSchema(t, "PostOrderRequest", submission)
	time.Sleep(2 * time.Millisecond)
	accepted := request(t, handler.HTTP(), http.MethodPost, "/v1/orders", submission, accessToken)
	receipt := readJSON[oif.SubmissionResponse](t, accepted)
	validateRawSchema(t, "PostOrderResponse", accepted.Body.Bytes())
	if receipt.Status != oif.Received {
		t.Fatal(receipt)
	}
	original, err := store.Record(t.Context(), receipt.OrderID)
	if err != nil {
		t.Fatal(err)
	}
	// A second handler has no local quote cache and uses the same durable store.
	restarted, _, _ := setup(t)
	restarted.Store = store
	replay := readJSON[oif.SubmissionResponse](t, request(t, restarted.HTTP(), http.MethodPost, "/v1/orders", submission, accessToken))
	if replay.OrderID != receipt.OrderID {
		t.Fatal("replay created another intent")
	}
	lease, err := store.Acquire(t.Context(), coordination.IntentResource(receipt.OrderID), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err = store.Advance(t.Context(), lease, receipt.OrderID, intent.Discovered, intent.Settled, "", true, 0); err != nil {
		t.Fatal(err)
	}
	status := request(t, restarted.HTTP(), http.MethodGet, "/v1/orders/"+receipt.OrderID, nil, accessToken)
	state := readJSON[oif.OrderResponse](t, status)
	validateRawSchema(t, "GetOrderResponse", status.Body.Bytes())
	if state.Status != oif.Finalized || state.CreatedAt != original.CreatedAt || state.UpdatedAt <= state.CreatedAt {
		t.Fatal(state)
	}
	time.Sleep(2 * time.Millisecond)
	assets := request(t, restarted.HTTP(), http.MethodGet, "/v1/assets", nil, accessToken)
	catalog := readJSON[oif.AssetsResponse](t, assets)
	validateRawSchema(t, "GetAssetsResponse", assets.Body.Bytes())
	if len(catalog.Networks) != 2 || catalog.Networks["84532"].Assets[0].Symbol != "USDC" {
		t.Fatal(catalog)
	}
}

func TestOIFRejectsUnsupportedAuthorizationAndChangedQuotes(t *testing.T) {
	handler, query, store := setup(t)
	q := readJSON[oif.QuoteResponse](t, request(t, handler.HTTP(), http.MethodPost, "/v1/quotes", query, accessToken)).Quotes[0]
	changed := q.Order
	changed.OpenIntentTx.GasRequired = "1"
	for _, submission := range []oif.Submission{
		{Order: q.Order, QuoteID: q.QuoteID, Signature: oif.Bytes{1}},
		{Order: q.Order, OriginSubmission: &oif.OriginSubmission{Mode: "protocol"}},
		{Order: changed, QuoteID: q.QuoteID},
		{Order: q.Order, QuoteID: q.QuoteID + "tampered"},
		{Order: oif.Order{Type: "oif-escrow-v0"}},
	} {
		time.Sleep(2 * time.Millisecond)
		if response := request(t, handler.HTTP(), http.MethodPost, "/v1/orders", submission, accessToken); response.Code != http.StatusBadRequest {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	if ids, err := store.Ready(t.Context(), 100, 0); err != nil || len(ids) != 0 {
		t.Fatal("rejected submission was persisted", ids, err)
	}
	for _, kind := range []oif.OrderType{"oif-escrow-v0", "oif-3009-v0", "oif-resource-lock-v0"} {
		handler, query, _ := setup(t)
		query.SupportedTypes = []oif.OrderType{kind}
		if response := request(t, handler.HTTP(), http.MethodPost, "/v1/quotes", query, accessToken); response.Code != http.StatusBadRequest {
			t.Fatal(response.Code)
		}
	}
	handler, query, _ = setup(t)
	handler.Enabled = false
	if response := request(t, handler.HTTP(), http.MethodPost, "/v1/quotes", query, accessToken); response.Code != http.StatusServiceUnavailable {
		t.Fatal(response.Code)
	}
}

func TestOIFHTTPAuthenticationPauseRateAndBounds(t *testing.T) {
	handler, _, _ := setup(t)
	server := handler.HTTP()
	if response := request(t, server, http.MethodGet, "/v1/assets", nil, "wrong"); response.Code != http.StatusUnauthorized {
		t.Fatal(response.Code)
	}
	handler.RequestsPerSecond = 1
	if response := request(t, server, http.MethodGet, "/v1/assets", nil, accessToken); response.Code != http.StatusOK {
		t.Fatal(response.Code)
	}
	if response := request(t, server, http.MethodGet, "/v1/assets", nil, accessToken); response.Code != http.StatusTooManyRequests {
		t.Fatal(response.Code)
	}
	handler, query, store := setup(t)
	if err := store.SetControl(t.Context(), 0, coordination.Control{Version: 1, Paused: true}); err != nil {
		t.Fatal(err)
	}
	if response := request(t, handler.HTTP(), http.MethodPost, "/v1/quotes", query, accessToken); response.Code != http.StatusServiceUnavailable {
		t.Fatal(response.Code)
	}
	for _, body := range []string{`{} {}`, `{"order":{"type":"oif-user-open-v0","openIntentTx":{"data":"AA=="}}}`, `{"signature":[256]}`, strings.Repeat(" ", 128<<10) + `{}`} {
		handler, _, _ := setup(t)
		req := httptest.NewRequest(http.MethodPost, "/v1/orders", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+accessToken)
		out := httptest.NewRecorder()
		handler.HTTP().ServeHTTP(out, req)
		if out.Code != http.StatusBadRequest {
			t.Fatal(out.Code, out.Body.String())
		}
	}
}
