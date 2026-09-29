package control

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/solver"
	"github.com/LuisUrrutia/goif-solver/internal/storage/memorystore"
)

func TestHealthMetricsAndControlAuthorization(t *testing.T) {
	handler := Handler(&solver.Service{}, "a-test-token")
	for _, tc := range []struct {
		path   string
		status int
	}{{"/healthz", 503}, {"/readyz", 503}, {"/metrics", 200}, {"/control", 401}, {"/intents/0x123", 401}} {
		req := httptest.NewRequest("GET", tc.path, nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Fatalf("%s: got %d", tc.path, rec.Code)
		}
	}
	req := httptest.NewRequest("PUT", "/control", nil)
	req.Header.Set("Authorization", "a-test-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 401 {
		t.Fatal("accepted malformed authorization")
	}
}

func TestProbesFollowEngineLifecycle(t *testing.T) {
	service := &solver.Service{Engine: &solver.Engine{Store: memorystore.New()}}
	handler := Handler(service, "")
	check := func(want int) {
		t.Helper()
		for _, path := range []string{"/healthz", "/readyz"} {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("GET", path, nil))
			if response.Code != want {
				t.Fatalf("%s: got %d, want %d", path, response.Code, want)
			}
		}
	}
	check(503)
	service.Publish = true
	if err := service.Run(t.Context()); err == nil {
		t.Fatal("missing publisher was accepted")
	}
	check(503)
	service.Publish = false
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	deadline := time.Now().Add(time.Second)
	for !service.Running() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	check(200)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	check(503)
}
