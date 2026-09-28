package lifi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWithdrawalReplacesRouteWithEmptyRanges(t *testing.T) {
	var got struct {
		Quotes []Quote `json:"quotes"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/quotes/submit" || r.Header.Get("X-API-Key") != "test-key" {
			t.Error("wrong request")
		}
		if e := json.NewDecoder(r.Body).Decode(&got); e != nil {
			t.Error(e)
		}
		w.Write([]byte(`{"status":"success","quotesAdded":0}`))
	}))
	defer server.Close()
	c, e := New(server.URL, "test-key", 1000)
	if e != nil {
		t.Fatal(e)
	}
	q := Quote{FromChain: "11155111", ToChain: "84532", FromAsset: "USDC", ToAsset: "USDC", Ranges: []Range{{MinAmount: "1", MaxAmount: "100", Quote: "0.99"}}, Expiry: time.Now().Add(time.Minute).Unix()}
	if e := c.Withdraw(t.Context(), q); e != nil {
		t.Fatal(e)
	}
	if len(got.Quotes) != 1 || got.Quotes[0].Ranges == nil || len(got.Quotes[0].Ranges) != 0 || got.Quotes[0].Expiry <= time.Now().Unix() {
		t.Fatalf("invalid withdrawal: %+v", got)
	}
	if len(q.Ranges) != 1 {
		t.Fatal("mutated caller quote")
	}
}
func TestRegistrationChallengeUsesDataEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"data":{"message":"register test nonce"}}`))
	}))
	defer server.Close()
	client, err := New(server.URL, "test", 1000)
	if err != nil {
		t.Fatal(err)
	}
	message, err := client.RegistrationMessage(t.Context())
	if err != nil || message != "register test nonce" {
		t.Fatalf("%q %v", message, err)
	}
}
