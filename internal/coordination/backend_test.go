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
	"github.com/LuisUrrutia/goif-solver/internal/intent"
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
		"QueueAndReservationMetricsFollowDurableState":   checkQueueMetrics,
		"OwnedOperationRenewsAndStopsOnLeaseLoss":        checkOwnedOperation,
		"DueSettlementPrecedesNewIntake":                 checkDueSettlementPrecedesNewIntake,
		"ReadyPagination":                                checkReadyPagination,
		"ExpiredAttemptKeepsEvidenceAndAllowsNewAttempt": checkExpiredAttemptKeepsEvidenceAndAllowsNewAttempt,
		"DurableTimestampsSurviveDuplicateDiscovery":     checkDurableTimestamps,
		"RetryAndControlIsolation":                       checkRetryAndControlIsolation,
		"ImmutableJournalAndBothFences":                  checkImmutableJournalAndBothFences,
		"DuplicateDiscoveryAndIndependentExecutor":       checkDuplicateDiscoveryAndIndependentExecutor,
		"ExpiredLeaseCannotAdvanceOrReleaseReplacement":  checkExpiredLeaseCannotAdvanceOrReleaseReplacement,
		"SignerReservationSurvivesLeaseLoss":             checkSignerReservationSurvivesLeaseLoss,
		"ConcurrentSignerLease":                          checkConcurrentSignerLease,
		"CheckpointCannotLoseConcurrentScannerProgress":  checkCheckpointCannotLoseConcurrentScannerProgress,
		"VersionedControlAndNodePrecedence":              checkVersionedControlAndNodePrecedence,
	}
	for name, factory := range factories {
		t.Run(name, func(t *testing.T) {
			for name, check := range cases {
				t.Run(name, func(t *testing.T) { check(t, factory(t)) })
			}
		})
	}
}

func checkQueueMetrics(t *testing.T, s coordination.Backend) {
	ctx := t.Context()
	if _, err := s.Enqueue(ctx, "settling", "payload"); err != nil {
		t.Fatal(err)
	}
	order := mustLease(t, s, coordination.IntentResource("settling"), time.Minute)
	signer := mustLease(t, s, coordination.SignerResource("network", "account"), time.Minute)
	tx := coordination.Transaction{Operation: "fill", Raw: "bytes", Hash: "hash", Codec: "test", Metadata: "{}"}
	if err := s.Prepare(ctx, order, signer, tx); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Millisecond)
	before, err := s.Stats(ctx)
	if err != nil || before.Outstanding != 1 || before.Due != 1 || before.OldestDueMillis <= 0 || before.PendingSigners != 1 || before.OldestPendingMillis <= 0 {
		t.Fatalf("missing durable work metrics: %+v %v", before, err)
	}
	if err := s.Prepare(ctx, order, signer, tx); err != nil {
		t.Fatal(err)
	}
	if err := s.Advance(ctx, order, "settling", intent.Discovered, intent.Discovered, "retry", false, time.Minute); err != nil {
		t.Fatal(err)
	}
	stats, err := s.Stats(ctx)
	if err != nil || stats.Due != 0 || stats.Outstanding != 1 || stats.PendingSigners != 1 || stats.OldestPendingMillis < before.OldestPendingMillis {
		t.Fatalf("retry reset reservation age: %+v %v", stats, err)
	}
	outcome := coordination.Outcome{State: coordination.Finalized, Evidence: "{}"}
	if err := s.CompleteTransaction(ctx, signer, tx.Operation, outcome); err != nil {
		t.Fatal(err)
	}
	tx.Operation = "claim"
	if err := s.Prepare(ctx, order, signer, tx); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTransaction(ctx, signer, "fill", outcome); err != nil {
		t.Fatal(err)
	}
	stats, err = s.Stats(ctx)
	if err != nil || stats.PendingSigners != 1 {
		t.Fatal("old completion removed current reservation from metrics", stats, err)
	}
	if err := s.CompleteTransaction(ctx, signer, tx.Operation, outcome); err != nil {
		t.Fatal(err)
	}
	if err := s.Advance(ctx, order, "settling", intent.Discovered, intent.Settled, "", true, 0); err != nil {
		t.Fatal(err)
	}
	stats, err = s.Stats(ctx)
	if err != nil || stats != (coordination.QueueStats{}) {
		t.Fatal("completed work remains in queue metrics", stats, err)
	}
}

func checkOwnedOperation(t *testing.T, s coordination.Backend) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	started, done := make(chan coordination.Lease, 1), make(chan error, 1)
	go func() {
		done <- coordination.RunOwned(ctx, s, "subscription", 300*time.Millisecond, func(ctx context.Context, lease coordination.Lease) error {
			started <- lease
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	var lease coordination.Lease
	select {
	case lease = <-started:
	case <-ctx.Done():
		t.Fatal("owner did not start")
	}
	time.Sleep(650 * time.Millisecond)
	if _, err := s.Acquire(ctx, lease.Resource, time.Second); !errors.Is(err, coordination.ErrBusy) {
		t.Fatal("idle ownership was not renewed", err)
	}
	if err := s.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
	replacement := mustLease(t, s, lease.Resource, time.Second)

	select {
	case err := <-done:
		if !errors.Is(err, coordination.ErrLeaseLost) {
			t.Fatal("lease loss was not propagated", err)
		}
	case <-ctx.Done():
		t.Fatal("former owner did not stop")
	}
	if err := s.Renew(ctx, replacement, time.Second); err != nil {
		t.Fatal("former owner released replacement", err)
	}
}

func checkDueSettlementPrecedesNewIntake(t *testing.T, s coordination.Backend) {
	ctx := t.Context()
	if _, err := s.Enqueue(ctx, "settlement", "payload"); err != nil {
		t.Fatal(err)
	}
	lease := mustLease(t, s, coordination.IntentResource("settlement"), time.Minute)
	if err := s.Advance(ctx, lease, "settlement", intent.Discovered, intent.Stage("filled"), "", false, 0); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	for i := range 150 {
		if _, err := s.Enqueue(ctx, fmt.Sprintf("fresh-%03d", i), "payload"); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := s.Ready(ctx, 100, 0)

	if err != nil || len(ids) != 100 || ids[0] != "settlement" {
		t.Fatalf("new intake overtook due settlement: %v %v", ids, err)
	}
}

func checkReadyPagination(t *testing.T, s coordination.Backend) {
	for i := range 5 {
		if _, err := s.Enqueue(t.Context(), fmt.Sprintf("intent-%d", i), "payload"); err != nil {
			t.Fatal(err)
		}
	}
	for offset, want := range map[int64]int{0: 2, 2: 2, 4: 1, 6: 0} {
		ids, err := s.Ready(t.Context(), 2, offset)
		if err != nil || len(ids) != want {
			t.Fatalf("offset %d: %v %v", offset, ids, err)
		}
		for i, id := range ids {
			if id != fmt.Sprintf("intent-%d", offset+int64(i)) {
				t.Fatalf("page lost order: %v", ids)
			}
		}
	}
	if _, err := s.Ready(t.Context(), 2, -1); err == nil {
		t.Fatal("accepted negative offset")
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
	ids, e := executor.Ready(ctx, 10, 0)
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
	ids, e = s.Ready(ctx, 10, 0)
	if e != nil || len(ids) != 0 {
		t.Fatal("terminal order remains ready")
	}
}

func checkExpiredLeaseCannotAdvanceOrReleaseReplacement(t *testing.T, s coordination.Backend) {
	ctx := t.Context()
	if _, err := s.Enqueue(ctx, "a", "payload"); err != nil {
		t.Fatal(err)
	}
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
	signer := mustLease(t, s, "signer:84532:alice", time.Second)
	tx := coordination.Transaction{Operation: "a:fill", Raw: "0x1234", Hash: "0xabcd", Codec: "test-v1", Metadata: `{"nonce":7}`}
	if e := s.Prepare(ctx, order, signer, tx); e != nil {
		t.Fatal(e)
	}
	if err := s.Renew(ctx, signer, 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(35 * time.Millisecond)
	next := mustLease(t, s, signer.Resource, time.Second)
	recovered, e := s.Transaction(ctx, signer.Resource, "a:fill")
	if e != nil || recovered != tx {
		t.Fatalf("journal: %+v %v", recovered, e)
	}
	other := mustLease(t, s, "order:b", time.Second)
	if e := s.Prepare(ctx, other, next, coordination.Transaction{Operation: "b:fill", Raw: "0x5678", Hash: "0xdcba", Codec: "test-v1", Metadata: `{"nonce":7}`}); !errors.Is(e, coordination.ErrBusy) {
		t.Fatalf("nonce reservation lost: %v", e)
	}
	if e := s.CompleteTransaction(ctx, signer, "a:fill", coordination.Outcome{State: coordination.Finalized, Evidence: `{"block":"canonical"}`}); !errors.Is(e, coordination.ErrLeaseLost) {
		t.Fatal(e)
	}
	if e := s.CompleteTransaction(ctx, next, "a:fill", coordination.Outcome{State: coordination.Finalized, Evidence: `{"block":"canonical"}`}); e != nil {
		t.Fatal(e)
	}
	if e := s.Prepare(ctx, other, next, coordination.Transaction{Operation: "b:fill", Raw: "0x5678", Hash: "0xdcba", Codec: "test-v1", Metadata: `{"nonce":8}`}); e != nil {
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
	started := time.Now()
	delay := 100 * time.Millisecond
	if err := s.Advance(ctx, lease, "retry", "discovered", "discovered", "waiting", false, delay); err != nil {
		t.Fatal(err)
	}
	if ids, err := s.Ready(ctx, 10, 0); err != nil || len(ids) != 0 && time.Since(started) < delay {
		t.Fatal("retry ran before deadline", ids, err)
	}
	time.Sleep(delay)
	if ids, err := s.Ready(ctx, 10, 0); err != nil || len(ids) != 1 {
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
	tx := coordination.Transaction{Operation: "fill", Raw: "signed-bytes", Hash: "hash", Codec: "test-v1", Metadata: `{"nonce":1}`}
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
	if err := s.CompleteTransaction(ctx, signer, tx.Operation, coordination.Outcome{State: coordination.Finalized, Evidence: `{"block":"canonical"}`}); err != nil {
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

func checkExpiredAttemptKeepsEvidenceAndAllowsNewAttempt(t *testing.T, s coordination.Backend) {
	ctx := t.Context()
	work := mustLease(t, s, coordination.IntentResource("protocol/native"), time.Minute)
	signer := mustLease(t, s, coordination.SignerResource("svm:devnet", "public-key"), time.Minute)
	first := coordination.Transaction{Operation: "fill/attempt-1", Codec: "expiring-test-v1", Raw: "signed-first", Hash: "first", Metadata: `{"last_valid_height":42}`}
	second := coordination.Transaction{Operation: "fill/attempt-2", Codec: first.Codec, Raw: "signed-second", Hash: "second", Metadata: `{"last_valid_height":99}`}
	if err := s.Prepare(ctx, work, signer, first); err != nil {
		t.Fatal(err)
	}
	if err := s.Prepare(ctx, work, signer, second); !errors.Is(err, coordination.ErrBusy) {
		t.Fatal("replaced an unresolved attempt", err)
	}
	if err := s.CompleteTransaction(ctx, signer, first.Operation, coordination.Outcome{State: coordination.Expired}); err == nil {
		t.Fatal("expiry without verified evidence released reservation")
	}
	outcome := coordination.Outcome{State: coordination.Expired, Evidence: `{"finalized_height":50,"signature_status":"absent"}`}

	if err := s.CompleteTransaction(ctx, signer, first.Operation, outcome); err != nil {
		t.Fatal(err)
	}
	if err := s.Prepare(ctx, work, signer, second); err != nil {
		t.Fatal(err)
	}

	if old, err := s.Transaction(ctx, signer.Resource, first.Operation); err != nil || old != first {
		t.Fatal("old immutable attempt lost", err)
	}
	if saved, err := s.TransactionOutcome(ctx, signer.Resource, first.Operation); err != nil || saved != outcome {
		t.Fatal("terminal evidence lost", err)
	}
	if err := s.CompleteTransaction(ctx, signer, first.Operation, outcome); err != nil {
		t.Fatal("idempotent completion failed", err)
	}
	if pending, err := s.Pending(ctx, signer.Resource); err != nil || pending != second.Operation {
		t.Fatal("old completion released new attempt", err)
	}
	if err := s.CompleteTransaction(ctx, signer, first.Operation, coordination.Outcome{State: coordination.Finalized, Evidence: `{}`}); !errors.Is(err, coordination.ErrConflict) {
		t.Fatal("terminal evidence overwritten", err)
	}
}

func checkDurableTimestamps(t *testing.T, s coordination.Backend) {
	ctx := t.Context()
	before := time.Now().Add(-time.Second).UnixMilli()
	if _, err := s.Enqueue(ctx, "timestamps", "payload"); err != nil {
		t.Fatal(err)
	}
	initial, err := s.Record(ctx, "timestamps")
	if err != nil {
		t.Fatal(err)
	}
	if initial.CreatedAt < before || initial.UpdatedAt != initial.CreatedAt {
		t.Fatal(initial)
	}
	time.Sleep(2 * time.Millisecond)
	if added, err := s.Enqueue(ctx, "timestamps", "payload"); err != nil || added {
		t.Fatal(added, err)
	}
	duplicate, err := s.Record(ctx, "timestamps")
	if err != nil || duplicate != initial {
		t.Fatal(duplicate, err)
	}
	lease := mustLease(t, s, coordination.IntentResource("timestamps"), time.Second)
	if err := s.Advance(ctx, lease, "timestamps", intent.Discovered, intent.Settled, "{}", true, 0); err != nil {
		t.Fatal(err)
	}
	settled, err := s.Record(ctx, "timestamps")
	if err != nil || settled.CreatedAt != initial.CreatedAt || settled.UpdatedAt <= initial.UpdatedAt {
		t.Fatal(settled, err)
	}
}
