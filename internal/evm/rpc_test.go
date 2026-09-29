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
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		result := "0x2a"
		if req.Method == "eth_chainId" {
			result = "0x539"
		}
		if err := json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
			t.Error(err)
		}
	}))
	defer good.Close()
	client, err := NewClient(t.Context(), []RPCSettings{{URL: bad.URL}, {URL: good.URL}}, 1337, 1000)
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		if req.Method != "eth_chainId" {
			operations.Add(1)
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`))
	}))
	defer server.Close()
	client, err := NewClient(t.Context(), []RPCSettings{{URL: server.URL}}, 1337, 1000)
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		out := map[string]interface{}{"jsonrpc": "2.0", "id": req.ID}
		if req.Method == "eth_chainId" {
			out["result"] = "0x539"
		} else {
			out["error"] = map[string]interface{}{"code": 3, "message": "execution reverted"}
		}
		if err := json.NewEncoder(w).Encode(out); err != nil {
			t.Error(err)
		}
	}))
	defer first.Close()
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		second.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer other.Close()
	client, err := NewClient(t.Context(), []RPCSettings{{URL: first.URL}, {URL: other.URL}}, 1337, 1000)
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
			if err := json.Unmarshal(body, &req); err != nil {
				t.Error(err)
				return
			}
			if req.Method == "eth_chainId" {
				if err := json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": "0x539"}); err != nil {
					t.Error(err)
				}
				return
			}
			mu.Lock()
			bodies = append(bodies, string(body))
			mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if err := json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": "0x1234"}); err != nil {
				t.Error(err)
			}
		}))
	}
	first, second := server(true), server(false)
	defer first.Close()
	defer second.Close()
	client, err := NewClient(t.Context(), []RPCSettings{{URL: first.URL}, {URL: second.URL}}, 1337, 1000)
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
		_, _ = io.Copy(io.Discard, r.Body)
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		value := "0x539"
		if req.Method != "eth_chainId" {
			value = "0x2a"
		}
		if err := json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": value}); err != nil {
			t.Error(err)
		}
	}))
	defer fast.Close()
	first, _ := url.Parse(slow.URL)
	second, _ := url.Parse(fast.URL)
	pool := &rpcTransport{base: http.DefaultTransport, chain: 1337, timeout: 500 * time.Millisecond, endpoints: []*rpcEndpoint{{url: first}, {url: second}}}
	request, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, slow.URL, strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"eth_blockNumber","params":[]}`))
	done := make(chan error, 1)
	go func() {
		res, err := pool.RoundTrip(request)
		if res != nil {
			_ = res.Body.Close()
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
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		value := "0x539"
		if req.Method != "eth_chainId" {
			if attempts.Add(1) == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			value = "0x2a"
		}
		if err := json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": value}); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	client, err := NewClient(t.Context(), []RPCSettings{{URL: server.URL}}, 1337, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	block, err := client.BlockNumber(t.Context())
	if err != nil || block != 42 || attempts.Load() != 2 {
		t.Fatalf("retry %d %d %v", block, attempts.Load(), err)
	}
}

func TestRPCBudgetExhaustionDoesNotStarveLaterProviders(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
	defer slow.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		value := "0x539"
		if req.Method != "eth_chainId" {
			value = "0x2a"
		}
		if err := json.NewEncoder(w).Encode(map[string]interface{}{"jsonrpc": "2.0", "id": req.ID, "result": value}); err != nil {
			t.Error(err)
		}
	}))
	defer good.Close()
	unavailable, _ := url.Parse(slow.URL)
	available, _ := url.Parse(good.URL)
	pool := &rpcTransport{base: http.DefaultTransport, chain: 1337, timeout: 250 * time.Millisecond, endpoints: []*rpcEndpoint{{url: unavailable}, {url: unavailable}, {url: available}}}
	request := func(ctx context.Context) *http.Request {
		r, _ := http.NewRequestWithContext(ctx, http.MethodPost, slow.URL, strings.NewReader(`{"jsonrpc":"2.0","id":3,"method":"eth_blockNumber","params":[]}`))
		return r
	}
	ctx, cancel := context.WithTimeout(t.Context(), 375*time.Millisecond)
	defer cancel()
	if _, err := pool.RoundTrip(request(ctx)); err == nil {
		t.Fatal("expected exhausted first-call budget")
	}
	// A later call starts beyond the timed-out prefix, rather than starving the
	// healthy provider forever behind the same per-call budget.
	ctx2, cancel2 := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel2()
	response, err := pool.RoundTrip(request(ctx2))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	data, _ := io.ReadAll(response.Body)
	if !strings.Contains(string(data), "0x2a") {
		t.Fatalf("later provider unavailable: %s", data)
	}
}
