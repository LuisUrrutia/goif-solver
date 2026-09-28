package evm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

const rpcBodyLimit = 8 << 20

// An endpoint is verified on first use, never by a startup fan-out. The mutex
// coalesces concurrent first users and assigns bounded request-rate slots.
type rpcEndpoint struct {
	url      *url.URL
	mu       sync.Mutex
	next     time.Time
	verified bool
	disabled bool
}
type rpcTransport struct {
	endpoints []*rpcEndpoint
	preferred atomic.Uint32
	chain     uint64
	interval  time.Duration
	timeout   time.Duration
	base      http.RoundTripper
}

func (t *rpcTransport) request(ctx context.Context, ep *rpcEndpoint, body []byte, header http.Header) (*http.Response, error) {
	ep.mu.Lock()
	at := ep.next
	if now := time.Now(); at.Before(now) {
		at = now
	}
	ep.next = at.Add(t.interval)
	ep.mu.Unlock()
	timer := time.NewTimer(time.Until(at))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.url.String(), bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid RPC request")
	}
	r.Header = header.Clone()
	return t.base.RoundTrip(r)
}
func readRPCResponse(res *http.Response) ([]byte, error) {
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, rpcBodyLimit+1))
	if err != nil || len(b) > rpcBodyLimit {
		return nil, errors.New("RPC response exceeds bounds or read failed")
	}
	return b, nil
}
func (t *rpcTransport) verify(ctx context.Context, ep *rpcEndpoint) error {
	// Verification uses a separate direct request to avoid recursively invoking
	// failover. Holding the endpoint lock only serializes its first chain check.
	ep.mu.Lock()
	defer ep.mu.Unlock()
	if ep.disabled {
		return errors.New("RPC chain ID mismatch")
	}
	if ep.verified {
		return nil
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.url.String(), bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}`))
	if err != nil {
		return errors.New("invalid RPC request")
	}
	r.Header.Set("Content-Type", "application/json")
	res, err := t.base.RoundTrip(r)
	if err != nil {
		return errors.New("RPC verification unavailable")
	}
	b, err := readRPCResponse(res)
	if err != nil || res.StatusCode != http.StatusOK {
		return errors.New("RPC verification unavailable")
	}
	var out struct {
		Result string `json:"result"`
	}
	if json.Unmarshal(b, &out) != nil {
		return errors.New("invalid RPC verification")
	}
	id, err := strconv.ParseUint(out.Result, 0, 64)
	if err != nil {
		return errors.New("invalid RPC chain ID")
	}
	if id != t.chain {
		ep.disabled = true
		return errors.New("RPC chain ID mismatch")
	}
	ep.verified = true
	return nil
}
func retryRPC(status int, body []byte) bool {
	if status == 429 || status == 408 || status >= 500 {
		return true
	}
	var response struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &response) == nil && response.Error != nil {
		return response.Error.Code == -32005 || response.Error.Code == -32603
	}
	return false
}
func (t *rpcTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, rpcBodyLimit+1))
	r.Body.Close()
	if err != nil || len(body) > rpcBodyLimit {
		return nil, errors.New("invalid RPC request body")
	}
	start := int(t.preferred.Load()) % len(t.endpoints)
	// One pass is bounded by endpoint count and the caller deadline. Raw signed
	// transactions are replayed byte-for-byte; deterministic EVM errors return.
	for attempt := 0; attempt < max(2, len(t.endpoints)); attempt++ {
		if err := r.Context().Err(); err != nil {
			return nil, err
		}
		index := (start + attempt) % len(t.endpoints)
		ep := t.endpoints[index]
		ctx, cancel := context.WithTimeout(r.Context(), t.timeout)
		if err = t.verify(ctx, ep); err != nil {
			cancel()
			continue
		}
		res, e := t.request(ctx, ep, body, r.Header)
		if e != nil {
			cancel()
			continue
		}
		data, e := readRPCResponse(res)
		cancel()
		if e != nil || retryRPC(res.StatusCode, data) {
			continue
		}
		t.preferred.Store(uint32(index))
		res.Body = io.NopCloser(bytes.NewReader(data))
		res.ContentLength = int64(len(data))
		return res, nil
	}
	return nil, errors.New("all configured RPC endpoints unavailable")
}

// NewClient constructs an HTTP RPC pool without opening a connection. Each
// endpoint is chain-checked before its first operation; failover is per call.
func NewClient(ctx context.Context, endpoints []string, chain uint64, rps int) (*ethclient.Client, error) {
	if len(endpoints) == 0 || len(endpoints) > 16 || chain == 0 || rps < 1 || rps > 1000 {
		return nil, errors.New("invalid RPC pool policy")
	}
	pool := &rpcTransport{chain: chain, interval: time.Second / time.Duration(rps), timeout: 5 * time.Second, base: http.DefaultTransport}
	for _, endpoint := range endpoints {
		u, err := url.Parse(endpoint)
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) {
			return nil, errors.New("invalid RPC endpoint")
		}
		pool.endpoints = append(pool.endpoints, &rpcEndpoint{url: u})
	}
	client := &http.Client{Transport: pool, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	conn, err := rpc.DialOptions(ctx, endpoints[0], rpc.WithHTTPClient(client))
	if err != nil {
		return nil, errors.New("construct RPC client failed")
	}
	return ethclient.NewClient(conn), nil
}
