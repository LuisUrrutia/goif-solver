package transport

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRateLimitCarriesRetryAfterWithoutSecretBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("private upstream detail"))
	}))
	defer server.Close()
	client, err := New(server.URL, nil, 1000)
	if err != nil {
		t.Fatal(err)
	}
	err = client.Do(t.Context(), "GET", "/", nil, nil)
	var status *StatusError
	if !errors.As(err, &status) || status.RetryAfter != time.Minute || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe or incomplete error: %v", err)
	}
}

func TestDoesNotForwardCredentialsOnRedirect(t *testing.T) {
	received := false
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { received = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	client, err := New(source.URL, http.Header{"Authorization": {"Bearer test"}}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Do(t.Context(), "GET", "/", nil, nil); err == nil || received {
		t.Fatal("redirect forwarded")
	}
}
