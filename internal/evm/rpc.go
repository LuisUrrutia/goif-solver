package evm

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

type rpcTransport struct {
	mu       sync.Mutex
	next     time.Time
	interval time.Duration
	base     http.RoundTripper
}

func (t *rpcTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	t.mu.Lock()
	at := t.next
	if now := time.Now(); at.Before(now) {
		at = now
	}
	t.next = at.Add(t.interval)
	t.mu.Unlock()
	timer := time.NewTimer(time.Until(at))
	defer timer.Stop()
	select {
	case <-r.Context().Done():
		return nil, r.Context().Err()
	case <-timer.C:
	}
	res, e := t.base.RoundTrip(r)
	if e != nil {
		return nil, errors.New("RPC transport failed")
	}
	return res, nil
}
func Dial(ctx context.Context, endpoint string, chain uint64, rps int) (*ethclient.Client, error) {
	u, e := url.Parse(endpoint)
	if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) {
		return nil, errors.New("invalid RPC endpoint")
	}
	if rps < 1 || rps > 1000 {
		return nil, errors.New("invalid RPC rate")
	}
	client := &http.Client{Timeout: 20 * time.Second, Transport: &rpcTransport{interval: time.Second / time.Duration(rps), base: http.DefaultTransport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	conn, e := rpc.DialOptions(ctx, endpoint, rpc.WithHTTPClient(client))
	if e != nil {
		return nil, errors.New("connect RPC failed")
	}
	c := ethclient.NewClient(conn)
	id, e := c.ChainID(ctx)
	if e != nil || !id.IsUint64() || id.Uint64() != chain {
		c.Close()
		return nil, errors.New("RPC chain ID mismatch or unavailable")
	}
	return c, nil
}
