package evm

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/transport"
)

func TestShortDeadlineDoesNotStarveHealthyRPC(t *testing.T) {
	for _, test := range []struct {
		name        string
		verified    bool
		stalledBody bool
	}{
		{name: "verification"},
		{name: "request", verified: true},
		{name: "response body", verified: true, stalledBody: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				slow, _ := url.Parse("http://slow.test")
				fast, _ := url.Parse("http://fast.test")
				calls := make(map[string]int)
				pool := &rpcTransport{timeout: 5 * time.Second, chain: 1}
				for _, endpoint := range []*url.URL{slow, fast} {
					pool.endpoints = append(pool.endpoints, &rpcEndpoint{url: endpoint, limiter: transport.NewLimiter(time.Millisecond), verified: test.verified})
				}
				pool.base = rpcRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls[r.URL.Host]++
					if r.URL.Host == slow.Host {
						if test.stalledBody {
							return &http.Response{StatusCode: http.StatusOK, Body: stalledRPCBody{ctx: r.Context()}, Request: r}, nil
						}
						<-r.Context().Done()
						return nil, r.Context().Err()
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":"0x1"}`)), Request: r}, nil
				})

				succeeded := 0
				for range 4 {
					ctx, cancel := context.WithTimeout(t.Context(), time.Second)
					req, _ := http.NewRequestWithContext(ctx, http.MethodPost, slow.String(), strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"eth_blockNumber","params":[]}`))
					res, err := pool.RoundTrip(req)
					cancel()
					if err == nil {
						succeeded++
					}
					if res != nil {
						_ = res.Body.Close()
					}
				}

				fastCalls := 4
				if !test.verified {
					fastCalls++
				}
				if succeeded != 4 || calls[slow.Host] != 1 || calls[fast.Host] != fastCalls {
					t.Fatalf("healthy fallback starved across four independent calls: calls=%v preferred=%d", calls, pool.preferred.Load())
				}
			})
		})
	}
}

type stalledRPCBody struct{ ctx context.Context }

func (b stalledRPCBody) Read([]byte) (int, error) { <-b.ctx.Done(); return 0, b.ctx.Err() }
func (stalledRPCBody) Close() error               { return nil }
