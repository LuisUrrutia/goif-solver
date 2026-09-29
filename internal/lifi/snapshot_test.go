package lifi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/storage/memorystore"
	"github.com/LuisUrrutia/goif-solver/internal/storage/redisstore"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestStreamLiveAcceptanceDoesNotWaitForSnapshot(t *testing.T) {
	snapshotStarted := make(chan struct{})
	liveAccepted := make(chan struct{})
	const raw = `{"meta":{"onChainOrderId":"overlap"},"order":{"expires":1755602241}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/orders" {
			close(snapshotStarted)
			select {
			case <-liveAccepted:
				_, _ = w.Write([]byte(`{"data":[` + raw + `],"meta":{"total":1,"offset":0}}`))
			case <-r.Context().Done():
			}
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		select {
		case <-snapshotStarted:
		case <-r.Context().Done():
			return
		}
		if err = conn.WriteJSON(notification{Event: pingEvent}); err != nil {
			t.Error(err)
			return
		}
		if err = conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Error(err)
			return
		}
		var pong notification
		if err = conn.ReadJSON(&pong); err != nil || pong.Event != pongEvent {
			t.Errorf("heartbeat during blocked snapshot: %v %+v", err, pong)
			return
		}
		if err = conn.WriteJSON(notification{Event: submitEvent, Data: json.RawMessage(raw)}); err != nil {
			t.Error(err)
			return
		}
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	source := snapshotSource(t, server.URL)
	var store coordination.Backend = memorystore.New()
	if addr := os.Getenv("TEST_REDIS_ADDR"); addr != "" {
		client := redis.NewClient(&redis.Options{Addr: addr})
		t.Cleanup(func() { _ = client.Close() })
		var err error
		store, err = redisstore.New(client, fmt.Sprintf("stream-overlap-%d", time.Now().UnixNano()))
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	deliveries, insertions := 0, 0

	err := source.Run(ctx, func(ctx context.Context, candidate intent.Candidate) error {
		inserted, err := store.Enqueue(ctx, candidate.ID, string(candidate.Payload))
		if err != nil {
			return err
		}
		if inserted {
			insertions++
		}
		deliveries++
		if deliveries == 1 {
			close(liveAccepted)
		} else {
			cancel()
		}
		return nil
	})

	if !errors.Is(err, context.Canceled) || deliveries != 2 || insertions != 1 {
		t.Fatalf("live/history overlap failed: deliveries=%d insertions=%d err=%v", deliveries, insertions, err)
	}
}

func TestStreamSnapshotRetriesWithoutDisconnecting(t *testing.T) {
	for _, failure := range []string{"unavailable", "throttled", "malformed page"} {
		t.Run(failure, func(t *testing.T) {
			var requests atomic.Int32
			firstRequest := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/orders" {
					if requests.Add(1) == 1 {
						close(firstRequest)
						switch failure {
						case "unavailable":
							w.WriteHeader(http.StatusServiceUnavailable)
						case "throttled":
							w.Header().Set("Retry-After", "1")
							w.WriteHeader(http.StatusTooManyRequests)
						case "malformed page":
							_, _ = w.Write([]byte(`{"data":false}`))
						}
						return
					}
					_, _ = w.Write([]byte(`{"data":[{"meta":{"onChainOrderId":"recovered"}}],"meta":{"total":1,"offset":0}}`))
					return
				}
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = conn.Close() }()
				<-firstRequest
				if err = conn.WriteJSON(notification{Event: submitEvent, Data: json.RawMessage(`{"meta":{"onChainOrderId":"live"}}`)}); err != nil {
					t.Error(err)
					return
				}
				if err = conn.SetReadDeadline(time.Now().Add(4 * time.Second)); err != nil {
					t.Error(err)
					return
				}
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()
			source := snapshotSource(t, server.URL)
			core, logs := observer.New(zap.InfoLevel)
			source.Log = zap.New(core)
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			seen := map[string]bool{}
			started := time.Now()

			err := source.Run(ctx, func(_ context.Context, candidate intent.Candidate) error {
				seen[candidate.ID] = true
				if len(seen) == 2 {
					cancel()
				}
				return nil
			})

			if !errors.Is(err, context.Canceled) || !seen["live"] || !seen["recovered"] || requests.Load() != 2 {
				t.Fatalf("snapshot did not recover alongside live events: seen=%v requests=%d err=%v", seen, requests.Load(), err)
			}
			if time.Since(started) < time.Second {
				t.Fatal("snapshot retry ignored backoff/Retry-After")
			}
			if logs.FilterMessage("LI.FI snapshot page unavailable").Len() != 1 || logs.FilterMessage("LI.FI snapshot incomplete; retrying with live intake active").Len() != 1 {
				t.Fatal("incomplete snapshot was not observable", logs.All())
			}
		})
	}
}

func TestStreamSkipsMalformedRecordsWithoutTruncatingPagination(t *testing.T) {
	var pages atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/orders" {
			pages.Add(1)
			if r.URL.Query().Get("offset") == "0" {
				_, _ = w.Write([]byte(`{"data":[{"order":{"expires":1.5}},` + strings.Repeat(`{"meta":{"onChainOrderId":"first"}},`, 48) + `{"meta":{"onChainOrderId":"first"}}],"meta":{"total":51,"offset":0}}`))
			} else {
				_, _ = w.Write([]byte(`{"data":[{"meta":{"onChainOrderId":"last"}}],"meta":{"total":51,"offset":50}}`))
			}
			return
		}
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = conn.Close() }()
		if err = conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Error(err)
			return
		}
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	source := snapshotSource(t, server.URL)
	core, logs := observer.New(zap.InfoLevel)
	source.Log = zap.New(core)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	deliveries := 0

	err := source.Run(ctx, func(_ context.Context, candidate intent.Candidate) error {
		deliveries++
		if candidate.ID == "last" {
			cancel()
		}
		return nil
	})

	if !errors.Is(err, context.Canceled) || deliveries != 50 || pages.Load() != 2 {
		t.Fatalf("invalid record blocked valid history: deliveries=%d pages=%d err=%v", deliveries, pages.Load(), err)
	}
	rejections := logs.FilterMessage("LI.FI snapshot records rejected").All()
	if len(rejections) != 1 || rejections[0].ContextMap()["rejected"] != int64(1) || logs.FilterMessage("LI.FI snapshot complete").Len() != 0 {
		t.Fatal("invalid history was reported as complete", logs.All())
	}
}

func TestStreamDurableFailureCancelsSnapshot(t *testing.T) {
	for _, delivery := range []string{"history", "live during blocked history"} {
		t.Run(delivery, func(t *testing.T) {
			requested := make(chan struct{})
			requestCanceled := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/orders" {
					close(requested)
					if delivery == "history" {
						_, _ = w.Write([]byte(`{"data":[{"meta":{"onChainOrderId":"history"}}],"meta":{"total":1,"offset":0}}`))
					} else {
						<-r.Context().Done()
						close(requestCanceled)
					}
					return
				}
				conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
				if err != nil {
					t.Error(err)
					return
				}
				defer func() { _ = conn.Close() }()
				<-requested
				if delivery != "history" {
					if err = conn.WriteJSON(notification{Event: submitEvent, Data: json.RawMessage(`{"meta":{"onChainOrderId":"live"}}`)}); err != nil {
						t.Error(err)
						return
					}
				}
				if err = conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
					t.Error(err)
					return
				}
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()
			source := snapshotSource(t, server.URL)
			core, logs := observer.New(zap.InfoLevel)
			source.Log = zap.New(core)
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			want := errors.New("durable storage unavailable")

			err := source.Run(ctx, func(context.Context, intent.Candidate) error { return want })

			if !errors.Is(err, want) || logs.FilterMessage("LI.FI snapshot complete").Len() != 0 {
				t.Fatalf("durable failure lost: err=%v logs=%v", err, logs.All())
			}
			if delivery != "history" {
				select {
				case <-requestCanceled:
				case <-ctx.Done():
					t.Fatal("snapshot HTTP request survived stream shutdown")
				}
			}
		})
	}
}

func snapshotSource(t *testing.T, serverURL string) Stream {
	t.Helper()
	api, err := New(serverURL, "", 1000)
	if err != nil {
		t.Fatal(err)
	}
	return Stream{URL: "ws" + strings.TrimPrefix(serverURL, "http"), API: api, Filters: []url.Values{{"status": {"Open"}}}, Resolve: func(_ context.Context, envelope Envelope) (intent.Candidate, error) {
		raw, err := json.Marshal(envelope.Intent())
		return intent.Candidate{ID: envelope.Meta.ID, Payload: raw}, err
	}}
}
