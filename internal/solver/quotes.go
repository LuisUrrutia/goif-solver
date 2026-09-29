package solver

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/quote"
)

type quoteLeases interface {
	Acquire(context.Context, string, time.Duration) (coordination.Lease, error)
	Renew(context.Context, coordination.Lease, time.Duration) error
	Release(context.Context, coordination.Lease) error
}

type Quoter struct {
	Store     quoteLeases
	Sources   []quote.Binding
	Enabled   bool
	Published func(string, quote.Offer)
}

const quoteControlInterval = 5 * time.Second

func (q *Quoter) publish(ctx context.Context, binding quote.Binding, withdraw bool) (time.Time, error) {
	if !q.Enabled {
		return time.Time{}, intent.ErrObserve
	}
	ctx, cancel := context.WithTimeout(ctx, 40*time.Second)
	defer cancel()
	lease, err := q.Store.Acquire(ctx, coordination.QuoteLease(binding.Name), 45*time.Second)
	if errors.Is(err, coordination.ErrBusy) {
		return time.Now().Add(quoteControlInterval), nil
	}
	if err != nil {
		return time.Time{}, err
	}
	defer func() {
		release, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = q.Store.Release(release, lease)
	}()
	offer, err := binding.Source.Offer(ctx, withdraw)
	if err != nil {
		return time.Time{}, err
	}
	if !time.Unix(offer.Expiry, 0).After(time.Now()) {
		return time.Time{}, errors.New("quote source returned an expired offer")
	}
	if err = q.Store.Renew(ctx, lease, 45*time.Second); err != nil {
		return time.Time{}, err
	}
	request, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err = binding.Publisher.PublishOffer(request, offer); err != nil {
		return time.Time{}, err
	}
	if q.Published != nil {
		q.Published(binding.Name, offer)
	}
	return renewalAt(time.Now(), time.Unix(offer.Expiry, 0)), nil
}

func renewalAt(now, expiry time.Time) time.Time {
	return now.Add(max(expiry.Sub(now)/2, time.Millisecond))
}

func (q *Quoter) Refresh(ctx context.Context, withdraw bool) error {
	failures := make([]error, len(q.Sources))
	var wg sync.WaitGroup
	for i, binding := range q.Sources {
		wg.Go(func() {
			_, err := q.publish(ctx, binding, withdraw)
			if err != nil {
				failures[i] = fmt.Errorf("quote %s: %w", binding.Name, err)
			}
		})
	}
	wg.Wait()
	return errors.Join(failures...)
}

// Each binding renews before its offer expires, independently of worker cadence
// and of inventory/API delays on other routes. Pause changes prompt withdrawal.
func (q *Quoter) Run(ctx context.Context, paused func(context.Context) (bool, error), failed func(error)) {
	var wg sync.WaitGroup
	for _, binding := range q.Sources {
		wg.Go(func() { q.runBinding(ctx, binding, paused, failed) })
	}
	wg.Wait()
}

func (q *Quoter) runBinding(ctx context.Context, binding quote.Binding, paused func(context.Context) (bool, error), failed func(error)) {
	var next time.Time
	var previous bool
	for ctx.Err() == nil {
		check, cancel := context.WithTimeout(ctx, quoteControlInterval)
		withdraw, err := paused(check)
		cancel()
		if err == nil && (withdraw != previous || !time.Now().Before(next)) {
			next, err = q.publish(ctx, binding, withdraw)
			previous = withdraw
		}
		delay := quoteControlInterval
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			failed(fmt.Errorf("quote %s: %w", binding.Name, err))
			next = time.Now().Add(max(quoteControlInterval, intent.RetryDelay(err)))
		} else if until := time.Until(next); until > 0 {
			delay = min(delay, until)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
