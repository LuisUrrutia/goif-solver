package redisstore

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
func mustLease(t *testing.T, s *Store, r string, ttl time.Duration) coordination.Lease {
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
	if _, e := s.Enqueue(ctx, "order-1", `{"amount":"200"}`); !errors.Is(e, coordination.ErrConflict) {
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
