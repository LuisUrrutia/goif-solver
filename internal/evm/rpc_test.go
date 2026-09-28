package evm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

func TestRPCFailoverReplaysIdenticalTransactionBytes(t *testing.T) {
	var bodies []string
	var mu sync.Mutex
	server := func(fail bool) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			var req struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			json.Unmarshal(body, &req)
			if req.Method == "eth_chainId" {
				json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": "0x539"})
				return
			}
			mu.Lock()
			bodies = append(bodies, string(body))
			mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": "0x1234"})
		}))
	}
	first, second := server(true), server(false)
	defer first.Close()
	defer second.Close()
	client, err := NewClient(t.Context(), []string{first.URL, second.URL}, 1337, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var result string
	if err = client.Client().CallContext(t.Context(), &result, "eth_sendRawTransaction", "0xdeadbeef"); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] {
		t.Fatalf("retry changed raw broadcast: %v", bodies)
	}
}
func TestRPCTimeoutFallsBackAndVerificationWaitCanCancel(t *testing.T) {
	started := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		select {
		case <-started:
		default:
			close(started)
		}
		<-r.Context().Done()
	}))
	defer slow.Close()
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		value := "0x539"
		if req.Method != "eth_chainId" {
			value = "0x2a"
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": value})
	}))
	defer fast.Close()
	first, _ := url.Parse(slow.URL)
	second, _ := url.Parse(fast.URL)
	pool := &rpcTransport{base: http.DefaultTransport, chain: 1337, timeout: 50 * time.Millisecond, endpoints: []*rpcEndpoint{{url: first}, {url: second}}}
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, slow.URL, strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"eth_blockNumber","params":[]}`))
	done := make(chan error, 1)
	go func() {
		res, err := pool.RoundTrip(request)
		if res != nil {
			res.Body.Close()
		}
		done <- err
	}()
	<-started
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	defer cancel()
	if err := pool.verify(ctx, pool.endpoints[0]); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("verification wait ignored deadline: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSingleRPCTransientFailureRetries(t *testing.T) {
	var attempts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		value := "0x539"
		if req.Method != "eth_chainId" {
			if attempts.Add(1) == 1 {
				w.WriteHeader(429)
				return
			}
			value = "0x2a"
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": value})
	}))
	defer server.Close()
	client, err := NewClient(t.Context(), []string{server.URL}, 1337, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	block, err := client.BlockNumber(t.Context())
	if err != nil || block != 42 || attempts.Load() != 2 {
		t.Fatalf("retry %d %d %v", block, attempts.Load(), err)
	}
}
