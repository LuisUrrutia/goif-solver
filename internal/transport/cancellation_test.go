package transport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type canceledBody struct {
	ctx     context.Context
	reading chan struct{}
}

func (b canceledBody) Read([]byte) (int, error) {
	close(b.reading)
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (canceledBody) Close() error { return nil }

var _ io.ReadCloser = canceledBody{}

func TestBodyCancellationRemainsClassifiable(t *testing.T) {
	client, err := New("http://127.0.0.1", nil, 1000)
	if err != nil {
		t.Fatal(err)
	}
	reading := make(chan struct{})
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: canceledBody{r.Context(), reading}, Request: r}, nil
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Do(ctx, http.MethodGet, "/", nil, nil) }()
	<-reading

	cancel()
	got := <-done

	if !errors.Is(got, context.Canceled) {
		t.Fatalf("body read lost cancellation identity: error=%q errors.Is(context.Canceled)=false", got)
	}
}

func TestCanceledReservationsDoNotDelayLaterTraffic(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	client, err := New(server.URL, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Do(t.Context(), http.MethodGet, "/", nil, nil); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for range 20 {
		_ = client.Do(canceled, http.MethodGet, "/", nil, nil)
	}

	ctx, stop := context.WithTimeout(t.Context(), 350*time.Millisecond)
	defer stop()
	got := client.Do(ctx, http.MethodGet, "/", nil, nil)

	if got != nil || requests.Load() != 2 {
		t.Fatalf("healthy request after 20 canceled requests: error=%v server_requests=%d (want success and 2)", got, requests.Load())
	}
}
