package lifi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/gorilla/websocket"
)

func TestStreamHandshakePreservesRetryAfter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	source := Stream{URL: "ws" + strings.TrimPrefix(server.URL, "http")}

	err := source.Run(t.Context(), func(context.Context, intent.Candidate) error { return nil })

	if intent.RetryDelay(err) != 10*time.Second {
		t.Fatal("WebSocket retry hint lost", err)
	}
}

func TestStreamIdentityIncludesFiltersButIgnoresTheirOrder(t *testing.T) {
	first := Stream{URL: "wss://example.test/", Filters: []url.Values{{"status": {"Signed", "Open"}}, {"originChainId": {"1"}}}}
	second := Stream{URL: first.URL, Filters: []url.Values{{"originChainId": {"1"}}, {"status": {"Open", "Signed"}}}}
	if first.Identity() != second.Identity() {
		t.Fatal("equivalent filters changed ownership")
	}
	second.Filters[0].Set("originChainId", "2")
	if first.Identity() == second.Identity() {
		t.Fatal("different filters share ownership")
	}
	if first.Filters[0]["status"][0] != "Signed" {
		t.Fatal("identity mutated filters")
	}
}

func TestStreamHeartbeatsAndSnapshotOverlap(t *testing.T) {
	upgraded := make(chan struct{})
	pong := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/orders" {
			select {
			case <-upgraded:
			default:
				t.Error("snapshot preceded subscription")
			}
			if err := json.NewEncoder(w).Encode(map[string]interface{}{"data": []map[string]interface{}{{"meta": map[string]string{"onChainOrderId": "same-intent"}}}, "meta": map[string]int{"total": 1, "offset": 0}}); err != nil {
				t.Error(err)
			}
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		close(upgraded)
		if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Error(err)
			return
		}
		if err := conn.WriteControl(websocket.PingMessage, []byte("control"), time.Now().Add(time.Second)); err != nil {
			t.Error(err)
			return
		}
		if err := conn.WriteJSON(notification{Event: pingEvent}); err != nil {
			t.Error(err)
			return
		}
		controlPong := false
		conn.SetPongHandler(func(string) error { controlPong = true; return nil })
		var response notification
		if err = conn.ReadJSON(&response); err != nil || response.Event != pongEvent || !controlPong {
			t.Errorf("heartbeat: %v %+v control=%v", err, response, controlPong)
			return
		}
		close(pong)
		data := json.RawMessage(`{"meta":{"onChainOrderId":"same-intent"}}`)
		if err := conn.WriteJSON(notification{Event: submitEvent, Data: data}); err != nil {
			t.Error(err)
			return
		}
		for {
			if _, _, err = conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	api, err := New(server.URL, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	source := Stream{URL: "ws" + strings.TrimPrefix(server.URL, "http"), API: api, Filters: []url.Values{{"status": {"Open"}}}, Resolve: func(_ context.Context, e Envelope) (intent.Candidate, error) {
		return intent.Candidate{ID: e.Meta.ID}, nil
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	count := 0
	err = source.Run(ctx, func(_ context.Context, c intent.Candidate) error {
		if c.ID != "same-intent" {
			t.Errorf("unexpected candidate %s", c.ID)
		}
		count++
		if count == 2 {
			cancel()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) || count != 2 {
		t.Fatalf("delivery/cancellation: %d %v", count, err)
	}
	select {
	case <-pong:
	default:
		t.Fatal("application heartbeat unanswered")
	}
}

func TestStreamPropagatesDurableAcceptanceFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if err := conn.WriteJSON(notification{Event: submitEvent, Data: json.RawMessage(`{}`)}); err != nil {
			t.Error(err)
			return
		}
		if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Error(err)
			return
		}
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	source := Stream{URL: "ws" + strings.TrimPrefix(server.URL, "http"), Resolve: func(context.Context, Envelope) (intent.Candidate, error) { return intent.Candidate{ID: "intent"}, nil }}
	want := errors.New("durability unavailable")
	if err := source.Run(t.Context(), func(context.Context, intent.Candidate) error { return want }); !errors.Is(err, want) {
		t.Fatalf("ack failure lost: %v", err)
	}
}
