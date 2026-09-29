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
	checking chan struct{}
	next     time.Time
	mu       sync.Mutex
	verified bool
	disabled bool
}
type rpcTransport struct {
	base      http.RoundTripper
	endpoints []*rpcEndpoint
	chain     uint64
	interval  time.Duration
	timeout   time.Duration
	preferred atomic.Int64
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
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(res.Body, rpcBodyLimit+1))
	if err != nil || len(b) > rpcBodyLimit {
		return nil, errors.New("RPC response exceeds bounds or read failed")
	}
	return b, nil
}

func (t *rpcTransport) verify(ctx context.Context, ep *rpcEndpoint) error {
	for {
		ep.mu.Lock()
		if ep.disabled {
			ep.mu.Unlock()
			return errors.New("RPC chain ID mismatch")
		}
		if ep.verified {
			ep.mu.Unlock()
			return nil
		}
		if pending := ep.checking; pending != nil {
			ep.mu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-pending:
				continue
			}
		}
		ep.checking = make(chan struct{})
		ep.mu.Unlock()
		mismatch, err := t.verifyChain(ctx, ep)
		ep.mu.Lock()
		ep.verified = err == nil
		ep.disabled = mismatch
		close(ep.checking)
		ep.checking = nil
		ep.mu.Unlock()
		return err
	}
}

func (t *rpcTransport) verifyChain(ctx context.Context, ep *rpcEndpoint) (bool, error) {
	res, err := t.request(ctx, ep, []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_chainId","params":[]}`), http.Header{"Content-Type": {"application/json"}})
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, errors.New("RPC verification unavailable")
	}
	b, err := readRPCResponse(res)
	if err != nil || res.StatusCode != http.StatusOK {
		return false, errors.New("RPC verification unavailable")
	}
	var out struct {
		Result string `json:"result"`
	}
	if json.Unmarshal(b, &out) != nil {
		return false, errors.New("invalid RPC verification")
	}
	id, err := strconv.ParseUint(out.Result, 0, 64)
	if err != nil {
		return false, errors.New("invalid RPC chain ID")
	}
	if id != t.chain {
		return true, errors.New("RPC chain ID mismatch")
	}
	return false, nil
}

func (t *rpcTransport) cooldown(ep *rpcEndpoint, header http.Header) {
	delay := max(t.interval, 250*time.Millisecond)
	if seconds, err := strconv.Atoi(header.Get("Retry-After")); err == nil && seconds > 0 {
		delay = max(delay, time.Duration(min(seconds, 60))*time.Second)
	}
	ep.mu.Lock()
	if until := time.Now().Add(delay); ep.next.Before(until) {
		ep.next = until
	}
	ep.mu.Unlock()
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
	_ = r.Body.Close()
	if err != nil || len(body) > rpcBodyLimit {
		return nil, errors.New("invalid RPC request body")
	}
	start := int(t.preferred.Load()) % len(t.endpoints)
	// One pass (two attempts for one provider) obeys the caller deadline. Signed
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
			t.cooldown(ep, nil)
			t.preferred.CompareAndSwap(int64(index), int64((index+1)%len(t.endpoints)))
			continue
		}
		res, e := t.request(ctx, ep, body, r.Header)
		if e != nil {
			cancel()
			t.cooldown(ep, nil)
			t.preferred.CompareAndSwap(int64(index), int64((index+1)%len(t.endpoints)))
			continue
		}
		data, e := readRPCResponse(res)
		cancel()
		if e != nil || retryRPC(res.StatusCode, data) {
			t.cooldown(ep, res.Header)
			t.preferred.CompareAndSwap(int64(index), int64((index+1)%len(t.endpoints)))
			continue
		}
		t.preferred.Store(int64(index))
		res.Body = io.NopCloser(bytes.NewReader(data))
		res.ContentLength = int64(len(data))
		return res, nil
	}
	if r.Context().Err() != nil {
		return nil, r.Context().Err()
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
