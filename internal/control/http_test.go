package control

import (
	"net/http/httptest"
	"testing"

	"github.com/LuisUrrutia/goif-solver/internal/solver"
)

func TestHealthMetricsAndControlAuthorization(t *testing.T) {
	handler := Handler(&solver.Service{}, "a-test-token")
	for _, tc := range []struct {
		path   string
		status int
	}{{"/healthz", 200}, {"/metrics", 200}, {"/control", 401}, {"/orders/0x123", 401}} {
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
