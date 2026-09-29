package coordination_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/storage/memorystore"
	"github.com/LuisUrrutia/goif-solver/internal/storage/redisstore"
	"github.com/redis/go-redis/v9"
)

func TestBackendContract(t *testing.T) {
	factories := map[string]func(*testing.T) coordination.Backend{
		"memory": func(t *testing.T) coordination.Backend { return memorystore.New() },
		"redis": func(t *testing.T) coordination.Backend {
			addr := os.Getenv("TEST_REDIS_ADDR")
			if addr == "" {
				t.Skip("run scripts/check.sh for real Redis contract tests")
			}
			client := redis.NewClient(&redis.Options{Addr: addr})
			t.Cleanup(func() { _ = client.Close() })
			namespace := fmt.Sprintf("contract-%d", time.Now().UnixNano())
			s, err := redisstore.New(client, namespace)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				keys, err := client.Keys(context.Background(), "{"+namespace+"}:*").Result()
				if err != nil {
					t.Error(err)
					return
				}
				if len(keys) > 0 {
					if err = client.Del(context.Background(), keys...).Err(); err != nil {
						t.Error(err)
					}
				}
			})
			return s
		},
	}
	cases := map[string]func(*testing.T, coordination.Backend){
		"RetryAndControlIsolation":                      checkRetryAndControlIsolation,
		"ImmutableJournalAndBothFences":                 checkImmutableJournalAndBothFences,
		"DuplicateDiscoveryAndIndependentExecutor":      checkDuplicateDiscoveryAndIndependentExecutor,
		"ExpiredLeaseCannotAdvanceOrReleaseReplacement": checkExpiredLeaseCannotAdvanceOrReleaseReplacement,
		"SignerReservationSurvivesLeaseLoss":            checkSignerReservationSurvivesLeaseLoss,
		"ConcurrentSignerLease":                         checkConcurrentSignerLease,
		"CheckpointCannotLoseConcurrentScannerProgress": checkCheckpointCannotLoseConcurrentScannerProgress,
		"VersionedControlAndNodePrecedence":             checkVersionedControlAndNodePrecedence,
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			for name, check := range cases {
				t.Run(name, func(t *testing.T) { check(t, factory(t)) })
			}
		})
	}
}
func mustLease(t *testing.T, s coordination.Backend, r string, ttl time.Duration) coordination.Lease {
	t.Helper()
	l, err := s.Acquire(t.Context(), r, ttl)
	if err != nil {
		t.Fatal(err)
	}
	return l
}
func checkDuplicateDiscoveryAndIndependentExecutor(t *testing.T, s coordination.Backend) {
	ctx := t.Context()
	var added atomic.Int32
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			ok, e := s.Enqueue(ctx, "order-1", `{"amount":"100"}`)
			if e != nil {
				t.Error(e)
			}
			if ok {
				added.Add(1)
			}
		})
	}
	wg.Wait()
	if added.Load() != 1 {
		t.Fatalf("enqueued %d times", added.Load())
	}
	if _, e := s.Enqueue(ctx, "order-1", `{"amount":"200"}`); !errors.Is(e, coordination.ErrConflict) {
		t.Fatalf("conflicting order: %v", e)
	}
	executor := s
	ids, e := executor.Ready(ctx, 10)
	if e != nil || len(ids) != 1 || ids[0] != "order-1" {
		t.Fatalf("ready %v: %v", ids, e)
	}
	l := mustLease(t, executor, "order:order-1", time.Second)
	if e := executor.Advance(ctx, l, "order-1", "discovered", "settled", "receipt", true, 0); e != nil {
		t.Fatal(e)
	}
	if ok, e := s.Enqueue(ctx, "order-1", `{"amount":"100"}`); e != nil || ok {
		t.Fatal("terminal order rediscovered")
	}
	ids, e = s.Ready(ctx, 10)
	if e != nil || len(ids) != 0 {
		t.Fatal("terminal order remains ready")
	}
}
func checkExpiredLeaseCannotAdvanceOrReleaseReplacement(t *testing.T, s coordination.Backend) {
	ctx := t.Context()
	s.Enqueue(ctx, "a", "payload")
	old := mustLease(t, s, "order:a", 20*time.Millisecond)
	time.Sleep(35 * time.Millisecond)
	current := mustLease(t, s, "order:a", time.Second)
	if current.Token <= old.Token {
		t.Fatal("fence did not increase")
	}
	if e := s.Advance(ctx, old, "a", "discovered", "filled", "", false, 0); !errors.Is(e, coordination.ErrLeaseLost) {
		t.Fatal(e)
	}
	if e := s.Release(ctx, old); !errors.Is(e, coordination.ErrLeaseLost) {
		t.Fatal(e)
	}
	if e := s.Advance(ctx, current, "a", "discovered", "validated", "", false, 0); e != nil {
		t.Fatal(e)
	}
	if e := s.Advance(ctx, current, "a", "discovered", "filled", "", false, 0); !errors.Is(e, coordination.ErrConflict) {
		t.Fatal(e)
	}
}
func checkSignerReservationSurvivesLeaseLoss(t *testing.T, s coordination.Backend) {
	ctx := t.Context()
	order := mustLease(t, s, "order:a", time.Second)
	signer := mustLease(t, s, "signer:84532:alice", 20*time.Millisecond)
	tx := coordination.Transaction{Operation: "a:fill", Raw: "0x1234", Hash: "0xabcd", Nonce: 7}
	if e := s.Prepare(ctx, order, signer, tx); e != nil {
		t.Fatal(e)
	}
	time.Sleep(35 * time.Millisecond)
	next := mustLease(t, s, signer.Resource, time.Second)
	recovered, e := s.Transaction(ctx, signer.Resource, "a:fill")
	if e != nil || recovered != tx {
		t.Fatalf("journal: %+v %v", recovered, e)
	}
	other := mustLease(t, s, "order:b", time.Second)
	if e := s.Prepare(ctx, other, next, coordination.Transaction{Operation: "b:fill", Raw: "0x5678", Hash: "0xdcba", Nonce: 7}); !errors.Is(e, coordination.ErrBusy) {
		t.Fatalf("nonce reservation lost: %v", e)
	}
	if e := s.CompleteTransaction(ctx, signer, "a:fill"); !errors.Is(e, coordination.ErrLeaseLost) {
		t.Fatal(e)
	}
	if e := s.CompleteTransaction(ctx, next, "a:fill"); e != nil {
		t.Fatal(e)
	}
	if e := s.Prepare(ctx, other, next, coordination.Transaction{Operation: "b:fill", Raw: "0x5678", Hash: "0xdcba", Nonce: 8}); e != nil {
		t.Fatal(e)
	}
}
func checkConcurrentSignerLease(t *testing.T, s coordination.Backend) {
	var acquired atomic.Int32
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			_, e := s.Acquire(t.Context(), "signer:1:alice", time.Second)
			if e == nil {
				acquired.Add(1)
			} else if !errors.Is(e, coordination.ErrBusy) {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	if acquired.Load() != 1 {
		t.Fatalf("%d concurrent nonce owners", acquired.Load())
	}
}

func checkCheckpointCannotLoseConcurrentScannerProgress(t *testing.T, s coordination.Backend) {
	store := s
	ctx := t.Context()
	if err := store.CommitCheckpoint(ctx, "chain", "", "block-10"); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitCheckpoint(ctx, "chain", "", "block-8"); !errors.Is(err, coordination.ErrConflict) {
		t.Fatalf("stale writer: %v", err)
	}
	value, err := store.Checkpoint(ctx, "chain")
	if err != nil || value != "block-10" {
		t.Fatalf("checkpoint rolled back %q %v", value, err)
	}
}
func checkVersionedControlAndNodePrecedence(t *testing.T, s coordination.Backend) {
	ctx := t.Context()
	c := coordination.Control{Version: 1, Nodes: map[string]coordination.NodeControl{"slow": {Workers: 1}, "maintenance": {Paused: true}}}
	if err := s.SetControl(ctx, 0, c); err != nil {
		t.Fatal(err)
	}
	if err := s.SetControl(ctx, 0, c); !errors.Is(err, coordination.ErrConflict) {
		t.Fatal("stale config write accepted")
	}
	read, err := s.Control(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.Allows("slow", 1, 4) || !read.Allows("fast", 1, 4) || read.Allows("maintenance", 0, 4) {
		t.Fatal("node precedence failed")
	}
	read.Paused = true
	read.Version = 2
	if err = s.SetControl(ctx, 1, read); err != nil {
		t.Fatal(err)
	}
	if read.Allows("fast", 0, 4) {
		t.Fatal("global pause overridden")
	}
	if err = s.BindConfig(ctx, "policy-a"); err != nil {
		t.Fatal(err)
	}
	if err = s.BindConfig(ctx, "policy-b"); !errors.Is(err, coordination.ErrConflict) {
		t.Fatal("incompatible fleet policy accepted")
	}
}

func checkRetryAndControlIsolation(t *testing.T, s coordination.Backend) {
	ctx := t.Context()
	if _, err := s.Enqueue(ctx, "retry", "payload"); err != nil {
		t.Fatal(err)
	}
	lease := mustLease(t, s, coordination.IntentResource("retry"), time.Second)
	if err := s.Advance(ctx, lease, "retry", "discovered", "discovered", "waiting", false, 40*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if ids, err := s.Ready(ctx, 10); err != nil || len(ids) != 0 {
		t.Fatal("retry ran before deadline", ids, err)
	}
	time.Sleep(60 * time.Millisecond)
	if ids, err := s.Ready(ctx, 10); err != nil || len(ids) != 1 {
		t.Fatal("retry lost", ids, err)
	}
	c := coordination.Control{Version: 1, Nodes: map[string]coordination.NodeControl{"a": {Workers: 1}}}
	if err := s.SetControl(ctx, 0, c); err != nil {
		t.Fatal(err)
	}
	c.Nodes["a"] = coordination.NodeControl{Workers: 10}
	read, err := s.Control(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.Nodes["a"].Workers != 1 {
		t.Fatal("write retained caller-owned map")
	}
	read.Nodes["a"] = coordination.NodeControl{Workers: 5}
	read, err = s.Control(ctx)
	if err != nil || read.Nodes["a"].Workers != 1 {
		t.Fatal("read leaked backend-owned map", err)
	}
}

func checkImmutableJournalAndBothFences(t *testing.T, s coordination.Backend) {
	ctx := t.Context()
	work := mustLease(t, s, coordination.IntentResource("a"), time.Second)
	signer := mustLease(t, s, "signer:chain:alice", time.Second)
	tx := coordination.Transaction{Operation: "fill", Raw: "signed-bytes", Hash: "hash", Nonce: 1}
	if err := s.Prepare(ctx, work, signer, tx); err != nil {
		t.Fatal(err)
	}
	changed := tx
	changed.Raw = "different"
	if err := s.Prepare(ctx, work, signer, changed); !errors.Is(err, coordination.ErrConflict) {
		t.Fatal("journal overwritten", err)
	}
	if err := s.Release(ctx, work); err != nil {
		t.Fatal(err)
	}
	if err := s.Prepare(ctx, work, signer, tx); !errors.Is(err, coordination.ErrLeaseLost) {
		t.Fatal("stale intent lease accepted", err)
	}
	if err := s.CompleteTransaction(ctx, signer, tx.Operation); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.Transaction(ctx, signer.Resource, tx.Operation)
	if err != nil || recovered != tx {
		t.Fatal("completion deleted immutable journal", err)
	}
	if pending, err := s.Pending(ctx, signer.Resource); err != nil || pending != "" {
		t.Fatal("completed signer still reserved", err)
	}
}
