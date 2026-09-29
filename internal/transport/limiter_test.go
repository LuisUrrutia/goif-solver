package transport

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestLimiterCanceledWaitersDoNotConsumeBudget(t *testing.T) {
	for _, waiting := range []bool{false, true} {
		name := "already canceled"
		if waiting {
			name = "canceled while waiting"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				limiter := NewLimiter(time.Second)
				start := time.Now()
				if err := limiter.Wait(t.Context()); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if !waiting {
					cancel()
				}
				done := make(chan error, 20)
				for range cap(done) {
					go func() { done <- limiter.Wait(ctx) }()
				}
				synctest.Wait()

				cancel()
				for range cap(done) {
					if err := <-done; !errors.Is(err, context.Canceled) {
						t.Fatalf("canceled waiter: %v", err)
					}
				}
				if err := limiter.Wait(t.Context()); err != nil {
					t.Fatal(err)
				}

				if elapsed := time.Since(start); elapsed != time.Second {
					t.Fatalf("canceled requests consumed budget: %s", elapsed)
				}
			})
		})
	}
}

func TestLimiterSpacesConcurrentRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const count = 32
		const interval = 10 * time.Millisecond
		limiter := NewLimiter(interval)
		start := time.Now()
		times := make(chan time.Time, count)
		failures := make(chan error, count)

		for range count {
			go func() {
				failures <- limiter.Wait(t.Context())
				times <- time.Now()
			}()
		}

		for i := range count {
			if err := <-failures; err != nil {
				t.Fatal(err)
			}
			if elapsed := (<-times).Sub(start); elapsed != time.Duration(i)*interval {
				t.Fatalf("request %d started at %s", i, elapsed)
			}
		}
	})
}

func TestLimiterCooldownExtendsAnExistingWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		limiter := NewLimiter(time.Second)
		start := time.Now()
		if err := limiter.Wait(t.Context()); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- limiter.Wait(t.Context()) }()
		synctest.Wait()

		time.Sleep(500 * time.Millisecond)
		limiter.Delay(2 * time.Second)
		limiter.Delay(time.Millisecond)
		if err := <-done; err != nil {
			t.Fatal(err)
		}

		if elapsed := time.Since(start); elapsed != 2500*time.Millisecond {
			t.Fatalf("cooldown changed or bypassed: %s", elapsed)
		}
	})
}
