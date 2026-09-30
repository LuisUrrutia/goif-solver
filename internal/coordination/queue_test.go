package coordination_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
)

func checkClaimNext(t *testing.T, store coordination.Backend) {
	const total = 80
	for i := range total {
		if _, err := store.Enqueue(t.Context(), fmt.Sprintf("item-%03d", i), "payload"); err != nil {
			t.Fatal(err)
		}
	}
	claims := make(chan coordination.Claim, total)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for {
				claim, err := store.ClaimNext(t.Context(), time.Minute)
				if errors.Is(err, coordination.ErrNotReady) {
					return
				}
				if err != nil {
					t.Error(err)
					return
				}
				claims <- claim
				if err = store.Advance(t.Context(), claim.Lease, claim.ID, intent.Discovered, intent.Settled, "", true, 0); err != nil {
					t.Error(err)
					return
				}
				if err = store.Release(t.Context(), claim.Lease); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	close(claims)

	seen := make(map[string]bool)
	for claim := range claims {
		if seen[claim.ID] || claim.Lease.Resource != coordination.IntentResource(claim.ID) {
			t.Fatal("duplicate or unbound ownership", claim)
		}
		seen[claim.ID] = true
	}
	if len(seen) != total {
		t.Fatal("work was lost", len(seen))
	}
}

func checkClaimNextRecovery(t *testing.T, store coordination.Backend) {
	if _, err := store.Enqueue(t.Context(), "recover", "payload"); err != nil {
		t.Fatal(err)
	}
	first, err := store.ClaimNext(t.Context(), 50*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Renew(t.Context(), first.Lease, 200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(70 * time.Millisecond)
	if _, err = store.ClaimNext(t.Context(), time.Second); !errors.Is(err, coordination.ErrNotReady) {
		t.Fatal("renewal did not hide active ownership", err)
	}
	if err = store.Wait(t.Context(), time.Second); err != nil {
		t.Fatal(err)
	}
	current, err := store.ClaimNext(t.Context(), time.Second)
	if err != nil || current.ID != first.ID || current.Lease.Token <= first.Lease.Token {
		t.Fatal("expired owner was not replaced", current, err)
	}
	if err = store.Release(t.Context(), first.Lease); !errors.Is(err, coordination.ErrLeaseLost) {
		t.Fatal("stale owner released replacement", err)
	}
	if err = store.Advance(t.Context(), first.Lease, first.ID, intent.Discovered, intent.Settled, "", true, 0); !errors.Is(err, coordination.ErrLeaseLost) {
		t.Fatal("stale owner changed recovered work", err)
	}
	if _, err = store.ClaimNext(t.Context(), time.Second); !errors.Is(err, coordination.ErrNotReady) {
		t.Fatal("stale owner made replacement available", err)
	}
	if err = store.Release(t.Context(), current.Lease); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimNext(t.Context(), time.Second); err != nil {
		t.Fatal("release failed to restore work", err)
	}
}

func checkQueueWakeup(t *testing.T, store coordination.Backend) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	done := make(chan error, 3)
	for range cap(done) {
		go func() { done <- store.Wait(ctx, time.Minute) }()
	}
	time.Sleep(30 * time.Millisecond)

	if _, err := store.Enqueue(ctx, "wake", "payload"); err != nil {
		t.Fatal(err)
	}
	for range cap(done) {
		if err := <-done; err != nil {
			t.Fatal("admission did not wake every waiter", err)
		}
	}
	// Admission before subscription must also be visible without a notification.
	if err := store.Wait(ctx, time.Minute); err != nil {
		t.Fatal("missed admission between scans", err)
	}
	claim, err := store.ClaimNext(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Advance(ctx, claim.Lease, claim.ID, intent.Discovered, intent.Settled, "", true, 0); err != nil {
		t.Fatal(err)
	}
	waiting, stop := context.WithCancel(ctx)
	go func() { done <- store.Wait(waiting, time.Minute) }()
	stop()
	if err := <-done; err == nil {
		t.Fatal("canceled wait succeeded")
	}
	started := time.Now()
	if err := store.Wait(ctx, 30*time.Millisecond); err != nil || time.Since(started) < 25*time.Millisecond {
		t.Fatal("empty queue did not honor reconciliation interval", err)
	}
}

func checkScheduledWakeup(t *testing.T, store coordination.Backend) {
	if _, err := store.Enqueue(t.Context(), "later", "payload"); err != nil {
		t.Fatal(err)
	}
	claim, err := store.ClaimNext(t.Context(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if err = store.Advance(t.Context(), claim.Lease, claim.ID, intent.Discovered, intent.Discovered, "waiting", false, 200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err = store.Release(t.Context(), claim.Lease); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ClaimNext(t.Context(), time.Minute); !errors.Is(err, coordination.ErrNotReady) {
		t.Fatal("retry ran before its deadline", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()

	if err = store.Wait(ctx, time.Minute); err != nil {
		t.Fatal("deferred deadline did not wake the queue", err)
	}
	current, err := store.ClaimNext(ctx, time.Minute)

	if err != nil || current.ID != claim.ID || time.Since(start) < 190*time.Millisecond {
		t.Fatal("invalid scheduled recovery", current, err)
	}
}
