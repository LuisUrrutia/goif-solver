package evm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/transport"
)

type rpcRoundTripFunc func(*http.Request) (*http.Response, error)

func (f rpcRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRPCPoolCancellationDoesNotPenalizeEndpoint(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		endpointURL, err := url.Parse("http://127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		requests := 0
		pool := &rpcTransport{base: rpcRoundTripFunc(func(r *http.Request) (*http.Response, error) {
			requests++
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
		})}
		ep := &rpcEndpoint{url: endpointURL, limiter: transport.NewLimiter(time.Second), verified: true}
		pool.endpoints = []*rpcEndpoint{ep}
		pool.timeout = 5 * time.Second
		request := func(ctx context.Context) error {
			r, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL.String(), strings.NewReader(`{}`))
			if err != nil {
				return err
			}
			response, err := pool.RoundTrip(r)
			if response != nil {
				_ = response.Body.Close()
			}
			return err
		}
		start := time.Now()
		if err := request(t.Context()); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 20)
		for range cap(done) {
			go func() { done <- request(ctx) }()
		}
		synctest.Wait()

		time.Sleep(500 * time.Millisecond)
		cancel()
		for range cap(done) {
			if err := <-done; !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		}
		if err := request(t.Context()); err != nil {
			t.Fatal(err)
		}

		if requests != 2 || time.Since(start) != time.Second {
			t.Fatalf("canceled RPC calls consumed budget: requests=%d elapsed=%s", requests, time.Since(start))
		}
	})
}
