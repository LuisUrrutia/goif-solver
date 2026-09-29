package main

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestServePropagatesEngineExit(t *testing.T) {
	failure := errors.New("engine unavailable")
	for _, result := range []error{failure, nil} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		server := &http.Server{Addr: "127.0.0.1:0", ReadHeaderTimeout: time.Second}
		stopped := make(chan struct{})
		server.RegisterOnShutdown(func() { close(stopped) })

		err := serve(ctx, server, func(context.Context) error { return result })

		cancel()
		if err == nil || result != nil && !errors.Is(err, failure) {
			t.Fatalf("engine exit was ignored: %v", err)
		}
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("HTTP server survived engine exit")
		}
	}
}

func TestServeStopsEngineWhenHTTPFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	stopped := make(chan struct{})
	server := &http.Server{Addr: "127.0.0.1:invalid", ReadHeaderTimeout: time.Second}

	err := serve(ctx, server, func(ctx context.Context) error {
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	})

	if err == nil {
		t.Fatal("HTTP failure was ignored")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("engine survived HTTP failure")
	}
}
