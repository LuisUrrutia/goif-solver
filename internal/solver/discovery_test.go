package solver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/storage/memorystore"
	"github.com/LuisUrrutia/goif-solver/internal/storage/redisstore"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

type independentExecutor struct{ store coordination.Backend }

func (*independentExecutor) Prepare(c intent.Candidate) (intent.Candidate, error) { return c, nil }
func (e *independentExecutor) Step(ctx context.Context, lease coordination.Lease, record coordination.Record) error {
	return e.store.Advance(ctx, lease, record.ID, record.Stage, intent.Settled, record.Payload, true, 0)
}
func (*independentExecutor) Recover(context.Context) error { return nil }
func TestIndependentProtocolDeduplicatesAndExecutes(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("run scripts/check.sh")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer func() { _ = client.Close() }()
	store, err := redisstore.New(client, fmt.Sprintf("generic-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	const independent intent.Kind = "independent-protocol"
	service := Service{Engine: &Engine{Store: store, Executors: map[intent.Kind]Executor{independent: &independentExecutor{store: store}}}, Log: zap.NewNop()}
	candidate := intent.Candidate{ID: "native-id-not-an-evm-hash", Kind: independent, Payload: json.RawMessage(`{"native":true}`)}
	for range 2 {
		if err = service.Accept(t.Context(), candidate); err != nil {
			t.Fatal(err)
		}
	}
	if service.Discovered.Load() != 1 {
		t.Fatal("duplicate accepted")
	}
	record, err := store.Record(t.Context(), candidate.Identity().Key())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Acquire(t.Context(), coordination.IntentResource(candidate.Identity().Key()), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = service.Engine.Step(t.Context(), lease, record); err != nil {
		t.Fatal(err)
	}
	record, err = store.Record(t.Context(), candidate.Identity().Key())
	if err != nil || record.Stage != intent.Settled || record.Detail != string(candidate.Payload) {
		t.Fatalf("strategy dispatch: %+v %v", record, err)
	}
	candidate.Kind = "unsupported"
	if err = service.Accept(t.Context(), candidate); err != nil {
		t.Fatal(err)
	}
	if service.Discovered.Load() != 1 {
		t.Fatal("unsupported strategy enqueued")
	}
}

type sourceFunc func(context.Context, intent.Emit) error

func (sourceFunc) Identity() intent.SourceID { return "test-source" }

func (f sourceFunc) Run(ctx context.Context, emit intent.Emit) error { return f(ctx, emit) }

func TestSourceReconnectHonorsRetryAfterAndCancellation(t *testing.T) {
	service := Service{Log: zap.NewNop()}
	ctx, cancel := context.WithTimeout(t.Context(), 1200*time.Millisecond)
	defer cancel()
	calls := 0
	err := sourceRetryHint{delay: 10 * time.Second}
	source := sourceFunc(func(context.Context, intent.Emit) error { calls++; return err })

	service.reconnectSource(ctx, source)

	if calls != 1 {
		t.Fatal("source reconnected before Retry-After", calls)
	}
	for range 100 {
		delay := retryDelay(time.Second, err)
		if delay < 10*time.Second || delay > 11*time.Second {
			t.Fatal("jitter violated provider deadline or bound", delay)
		}
	}
}

type sourceRetryHint struct{ delay time.Duration }

func (e sourceRetryHint) Error() string             { return "rate limited" }
func (e sourceRetryHint) RetryDelay() time.Duration { return e.delay }
func TestSourceReconnectReplaysThroughDurableDeduplication(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("run scripts/check.sh")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer func() { _ = client.Close() }()
	store, err := redisstore.New(client, fmt.Sprintf("reconnect-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	const kind intent.Kind = "other-protocol"
	service := Service{Engine: &Engine{Store: store, Executors: map[intent.Kind]Executor{kind: &independentExecutor{store: store}}}, Log: zap.NewNop()}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	sessions := 0
	source := sourceFunc(func(ctx context.Context, emit intent.Emit) error {
		sessions++
		if err := emit(ctx, intent.Candidate{ID: "replayed", Kind: kind, Payload: json.RawMessage(`{}`)}); err != nil {
			return err
		}
		if sessions == 2 {
			cancel()
		}
		return errors.New("connection closed")
	})
	service.runSource(ctx, source)
	if sessions != 2 || service.Discovered.Load() != 1 {
		t.Fatalf("reconnect/dedup sessions=%d discoveries=%d", sessions, service.Discovered.Load())
	}
}

func TestNativeIdentityIsScopedByProtocolAndDeduplicatedAcrossSources(t *testing.T) {
	store := memorystore.New()
	a, b := intent.Kind("protocol-a"), intent.Kind("protocol-b")
	service := Service{Engine: &Engine{Store: store, Executors: map[intent.Kind]Executor{a: &independentExecutor{store}, b: &independentExecutor{store}}}, Log: zap.NewNop()}
	for _, kind := range []intent.Kind{a, b, a, b} {
		if err := service.Accept(t.Context(), intent.Candidate{Kind: kind, ID: "same/native", Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	if service.Discovered.Load() != 2 {
		t.Fatal("protocol collision or duplicate delivery", service.Discovered.Load())
	}
	for _, kind := range []intent.Kind{a, b} {
		identity := intent.Identity{Kind: kind, NativeID: "same/native"}
		if parsed, err := intent.ParseIdentity(identity.Key()); err != nil || parsed != identity {
			t.Fatal("identity is not reversible", err)
		}
		record, err := store.Record(t.Context(), identity.Key())
		if err != nil {
			t.Fatal(err)
		}
		lease, err := store.Acquire(t.Context(), coordination.IntentResource(record.ID), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err = service.Engine.Step(t.Context(), lease, record); err != nil {
			t.Fatal(err)
		}
	}
}
