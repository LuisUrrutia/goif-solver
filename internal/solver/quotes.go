package solver

import (
	"context"
	"errors"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/quote"
)

type Quoter struct {
	Store     coordination.Backend
	Publisher quote.Publisher
	Sources   []quote.Binding
	Enabled   bool
}

func (q *Quoter) Refresh(ctx context.Context, withdraw bool) error {
	if !q.Enabled {
		return intent.ErrObserve
	}
	lease, err := q.Store.Acquire(ctx, coordination.QuoteResource, 45*time.Second)
	if errors.Is(err, coordination.ErrBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = q.Store.Release(context.WithoutCancel(ctx), lease) }()
	for _, binding := range q.Sources {
		offer, err := binding.Source.Offer(ctx, withdraw)
		if err != nil {
			return err
		}
		if err = q.Store.Renew(ctx, lease, 45*time.Second); err != nil {
			return err
		}
		request, cancel := context.WithTimeout(ctx, 20*time.Second)
		err = q.Publisher.PublishOffer(request, offer)
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}
