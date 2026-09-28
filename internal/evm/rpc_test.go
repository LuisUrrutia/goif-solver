package evm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestRPCPoolIsLazyAndFailsOver(t *testing.T) {
	var calls atomic.Int64
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "unavailable", 503) }))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		result := "0x2a"
		if req.Method == "eth_chainId" {
			result = "0x539"
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	defer good.Close()
	client, err := NewClient(t.Context(), []string{bad.URL, good.URL}, 1337, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if calls.Load() != 0 {
		t.Fatal("constructor contacted RPC")
	}
	block, err := client.BlockNumber(t.Context())
	if err != nil || block != 42 {
		t.Fatalf("failover: %d %v", block, err)
	}
	before := calls.Load()
	if _, err = client.BlockNumber(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != before+1 {
		t.Fatal("healthy endpoint not reused or chain ID rechecked")
	}
}
func TestRPCPoolRejectsWrongChainBeforeOperation(t *testing.T) {
	var operations atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Method != "eth_chainId" {
			operations.Add(1)
		}
		w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
	}))
	defer server.Close()
	client, err := NewClient(t.Context(), []string{server.URL}, 1337, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err = client.BlockNumber(t.Context()); err == nil {
		t.Fatal("wrong-chain provider accepted")
	}
	if operations.Load() != 0 {
		t.Fatal("operation reached wrong chain")
	}
}
func TestRPCPoolDoesNotRetryReverts(t *testing.T) {
	var second atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		out := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID}
		if req.Method == "eth_chainId" {
			out["result"] = "0x539"
		} else {
			out["error"] = map[string]interface{}{"code": 3, "message": "execution reverted"}
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer first.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { second.Add(1); w.WriteHeader(500) }))
	defer other.Close()
	client, err := NewClient(t.Context(), []string{first.URL, other.URL}, 1337, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err = client.BlockNumber(t.Context()); err == nil {
		t.Fatal("expected RPC error")
	}
	if second.Load() != 0 {
		t.Fatal("deterministic error retried")
	}
}
