package app

import (
	"context"
	"errors"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/quote"
)

type Quoter struct {
	Config config.Config
	Engine *escrow.Engine
	API    quote.Publisher
}

func (s *Quoter) Refresh(ctx context.Context, withdraw bool) error {
	if !s.Engine.Execute {
		return intent.ErrObserve
	}
	lease, err := s.Engine.Store.Acquire(ctx, coordination.QuoteResource, 45*time.Second)
	if errors.Is(err, coordination.ErrBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = s.Engine.Store.Release(context.WithoutCancel(ctx), lease) }()
	for _, route := range s.Engine.Config.Routes {
		disabled := withdraw
		if !disabled {
			sender := s.Engine.Senders[route.Signer][route.DestinationChain]
			balance, err := evm.Balance(ctx, sender.Client, route.OutputToken, sender.Signer.Address())
			if err != nil {
				return err
			}
			cap, _ := evm.Uint(route.MaxOutput, 256)
			disabled = balance.Cmp(cap) < 0
		}
		quote, err := escrow.Quote(s.Config, route, disabled)
		if err != nil {
			return err
		}
		if err = s.Engine.Store.Renew(ctx, lease, 45*time.Second); err != nil {
			return err
		}
		request, cancel := context.WithTimeout(ctx, 20*time.Second)
		err = s.API.PublishOffer(request, quote)
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}
