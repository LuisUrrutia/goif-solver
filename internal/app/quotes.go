package app

import (
	"context"
	"errors"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/quote"
	"github.com/LuisUrrutia/goif-solver/internal/solver"
)

func (r *Runtime) quoter(c config.Config, store coordination.Backend, enabled bool) (*solver.Quoter, error) {
	if len(c.Publications) == 0 {
		return nil, nil
	}
	bindings := make([]quote.Binding, 0, len(c.Publications))
	for _, publication := range c.Publications {
		execution := r.Executions[publication.Route.Protocol]
		provider := r.Providers[publication.Provider]
		publisher := provider.publisher
		if execution == nil || publisher == nil {
			return nil, errors.New("publication adapter unavailable")
		}
		source := execution.Quotes[publication.Route.Name]
		if source == nil {
			return nil, errors.New("publication route unavailable")
		}
		bindings = append(bindings, quote.Binding{Name: publication.Provider + "/" + publication.Route.Key(), Source: checkedSource{source: source, provider: provider, route: publication.Route}, Publisher: publisher})
	}
	return &solver.Quoter{Store: store, Sources: bindings, Enabled: enabled}, nil
}

type checkedSource struct {
	source   quote.Source
	provider providerSet
	route    config.Route
}

func (s checkedSource) Offer(ctx context.Context, withdraw bool) (quote.Offer, error) {
	if s.provider.authorize != nil {
		if err := s.provider.authorize(); err != nil {
			return quote.Offer{}, err
		}
	}
	if !withdraw && s.provider.verify != nil {
		if err := s.provider.verify(ctx, s.route); err != nil {
			return quote.Offer{}, err
		}
	}
	return s.source.Offer(ctx, withdraw)
}
