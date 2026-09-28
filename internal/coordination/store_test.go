package coordination

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("run scripts/check.sh for real Redis integration tests")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	s, e := New(client, fmt.Sprintf("test-%d", time.Now().UnixNano()))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		keys, e := client.Keys(context.Background(), s.prefix+"*").Result()
		if e == nil && len(keys) > 0 {
			client.Del(context.Background(), keys...)
		}
	})
	return s
}
func mustLease(t *testing.T, s *Store, r string, ttl time.Duration) Lease {
	t.Helper()
	l, e := s.Acquire(t.Context(), r, ttl)
	if e != nil {
		t.Fatal(e)
	}
	return l
}
func TestDuplicateDiscoveryAndIndependentExecutor(t *testing.T) {
	s := testStore(t)
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
	if _, e := s.Enqueue(ctx, "order-1", `{"amount":"200"}`); !errors.Is(e, ErrConflict) {
		t.Fatalf("conflicting order: %v", e)
	}
	executor, _ := New(s.client, s.prefix[1:len(s.prefix)-2])
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
func TestExpiredLeaseCannotAdvanceOrReleaseReplacement(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	s.Enqueue(ctx, "a", "payload")
	old := mustLease(t, s, "order:a", 20*time.Millisecond)
	time.Sleep(35 * time.Millisecond)
	current := mustLease(t, s, "order:a", time.Second)
	if current.Token <= old.Token {
		t.Fatal("fence did not increase")
	}
	if e := s.Advance(ctx, old, "a", "discovered", "filled", "", false, 0); !errors.Is(e, ErrLeaseLost) {
		t.Fatal(e)
	}
	if e := s.Release(ctx, old); !errors.Is(e, ErrLeaseLost) {
		t.Fatal(e)
	}
	if e := s.Advance(ctx, current, "a", "discovered", "validated", "", false, 0); e != nil {
		t.Fatal(e)
	}
	if e := s.Advance(ctx, current, "a", "discovered", "filled", "", false, 0); !errors.Is(e, ErrConflict) {
		t.Fatal(e)
	}
}
func TestSignerReservationSurvivesLeaseLoss(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	order := mustLease(t, s, "order:a", time.Second)
	signer := mustLease(t, s, "signer:84532:alice", 20*time.Millisecond)
	tx := Transaction{Operation: "a:fill", Raw: "0x1234", Hash: "0xabcd", Nonce: 7}
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
	if e := s.Prepare(ctx, other, next, Transaction{Operation: "b:fill", Raw: "0x5678", Hash: "0xdcba", Nonce: 7}); !errors.Is(e, ErrBusy) {
		t.Fatalf("nonce reservation lost: %v", e)
	}
	if e := s.CompleteTransaction(ctx, signer, "a:fill"); !errors.Is(e, ErrLeaseLost) {
		t.Fatal(e)
	}
	if e := s.CompleteTransaction(ctx, next, "a:fill"); e != nil {
		t.Fatal(e)
	}
	if e := s.Prepare(ctx, other, next, Transaction{Operation: "b:fill", Raw: "0x5678", Hash: "0xdcba", Nonce: 8}); e != nil {
		t.Fatal(e)
	}
}
func TestConcurrentSignerLease(t *testing.T) {
	s := testStore(t)
	var acquired atomic.Int32
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			_, e := s.Acquire(t.Context(), "signer:1:alice", time.Second)
			if e == nil {
				acquired.Add(1)
			} else if !errors.Is(e, ErrBusy) {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	if acquired.Load() != 1 {
		t.Fatalf("%d concurrent nonce owners", acquired.Load())
	}
}
