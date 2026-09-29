package app

import (
	"context"
	"errors"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/quote"
	"go.uber.org/zap"
)

func PublishQuotes(ctx context.Context, c config.Config, withdraw bool, published func(string, quote.Offer), failed func(error)) error {
	store, closeStore, err := OpenStore(c)
	if err != nil {
		return err
	}
	defer closeStore()
	runtime, err := Open(ctx, c, store, false, zap.NewNop(), builtins())
	if err != nil {
		return err
	}
	defer runtime.Close()
	q, err := runtime.quoter(c, store, true)
	if err != nil {
		return err
	}
	if q == nil {
		return errors.New("no publications configured")
	}
	q.Published = published
	if withdraw {
		return q.Refresh(ctx, true)
	}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		if err := q.Refresh(shutdown, true); err != nil {
			failed(err)
		}
	}()
	q.Run(ctx, func(ctx context.Context) (bool, error) { state, err := store.Control(ctx); return state.Paused, err }, failed)
	return nil
}

func RegisterProviders(ctx context.Context, c config.Config) error {
	runtime, err := Open(ctx, c, nil, false, zap.NewNop(), builtins())
	if err != nil {
		return err
	}
	defer runtime.Close()
	if len(runtime.Providers) == 0 {
		return errors.New("no providers selected")
	}
	for _, provider := range runtime.Providers {
		if provider.register == nil {
			return errors.New("selected provider has no registration operation")
		}
		if err = provider.register(ctx); err != nil {
			return err
		}
	}
	return nil
}
