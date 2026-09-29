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

type Quoter struct {
	Store     coordination.Leases
	Published func(string, quote.Offer)
	Sources   []quote.Binding
	Enabled   bool
}

const (
	quoteControlInterval = time.Second
	quoteLeaseTTL        = 15 * time.Second
)

func (q *Quoter) publish(ctx context.Context, binding quote.Binding, lease coordination.Lease, withdraw bool) (time.Time, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	offer, err := binding.Source.Offer(ctx, withdraw)
	if err != nil {
		return time.Time{}, err
	}
	if !time.Unix(offer.Expiry, 0).After(time.Now()) {
		return time.Time{}, errors.New("quote source returned an expired offer")
	}
	if err = q.Store.Renew(ctx, lease, quoteLeaseTTL); err != nil {
		return time.Time{}, err
	}
	request, cancel := context.WithTimeout(ctx, 10*time.Second)
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
	return now.Add(max(expiry.Sub(now)/3, time.Millisecond))
}

func (q *Quoter) Refresh(ctx context.Context, withdraw bool) error {
	if !q.Enabled {
		return intent.ErrObserve
	}
	failures := make([]error, len(q.Sources))
	var wg sync.WaitGroup
	for i, binding := range q.Sources {
		wg.Go(func() {
			err := coordination.RunOwned(ctx, q.Store, coordination.QuoteLease(binding.Name), quoteLeaseTTL, func(ctx context.Context, lease coordination.Lease) error {
				_, err := q.publish(ctx, binding, lease, withdraw)
				return err
			})
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
	if !q.Enabled {
		failed(intent.ErrObserve)
		return
	}
	var wg sync.WaitGroup
	for _, binding := range q.Sources {
		wg.Go(func() {
			for ctx.Err() == nil {
				err := coordination.RunOwned(ctx, q.Store, coordination.QuoteLease(binding.Name), quoteLeaseTTL, func(ctx context.Context, lease coordination.Lease) error {
					return q.runBinding(ctx, binding, lease, paused, failed)
				})
				if ctx.Err() != nil {
					return
				}
				if !errors.Is(err, coordination.ErrBusy) {
					failed(fmt.Errorf("quote %s ownership: %w", binding.Name, err))
				}
				if !wait(ctx, retryDelay(quoteControlInterval, err)) {
					return
				}
			}
		})
	}
	wg.Wait()
}

func (q *Quoter) runBinding(ctx context.Context, binding quote.Binding, lease coordination.Lease, paused func(context.Context) (bool, error), failed func(error)) error {
	var next, retryAt time.Time
	var previous bool
	for ctx.Err() == nil {
		check, cancel := context.WithTimeout(ctx, quoteControlInterval)
		withdraw, err := paused(check)
		cancel()
		if err == nil && !time.Now().Before(retryAt) && (withdraw != previous || !time.Now().Before(next)) {
			next, err = q.publish(ctx, binding, lease, withdraw)
			previous = withdraw
		}
		delay := quoteControlInterval
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failed(fmt.Errorf("quote %s: %w", binding.Name, err))
			retryAt = time.Now().Add(retryDelay(quoteControlInterval, err))
			next = retryAt
		} else if until := time.Until(next); until > 0 {
			delay = min(delay, until)
		}
		if !wait(ctx, delay) {
			return ctx.Err()
		}
	}
	return ctx.Err()
}
