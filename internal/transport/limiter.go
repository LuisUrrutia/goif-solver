package transport

import (
	"context"
	"sync"
	"time"
)

// Limiter spaces request starts without reserving slots for waiting callers.
type Limiter struct {
	next     time.Time
	waiter   chan struct{}
	interval time.Duration
	mu       sync.Mutex
}

func NewLimiter(interval time.Duration) *Limiter {
	return &Limiter{interval: interval, waiter: make(chan struct{}, 1)}
}

func (l *Limiter) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case l.waiter <- struct{}{}:
		defer func() { <-l.waiter }()
	}
	for {
		l.mu.Lock()
		if err := ctx.Err(); err != nil {
			l.mu.Unlock()
			return err
		}
		delay := time.Until(l.next)
		if delay <= 0 {
			l.next = time.Now().Add(l.interval)
			l.mu.Unlock()
			return nil
		}
		l.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Delay extends the cooldown, including for a caller already waiting for a turn.
func (l *Limiter) Delay(delay time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if until := time.Now().Add(max(delay, l.interval)); l.next.Before(until) {
		l.next = until
	}
}
