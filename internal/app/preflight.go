package app

import (
	"context"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/preflight"
)

func Preflight(ctx context.Context, c config.Config) (preflight.Report, error) {
	providers, err := configureProviders(c, nil)
	if err != nil {
		return preflight.Report{}, err
	}
	if providers.catalog != nil {
		if err = providers.catalog(ctx); err != nil {
			return preflight.Report{}, err
		}
	}
	clients, closeClients, err := openClients(ctx, c)
	if err != nil {
		return preflight.Report{}, err
	}
	defer closeClients()
	backends, err := configureSettlements(c, clients, nil, false)
	if err != nil {
		return preflight.Report{}, err
	}
	return preflight.Run(ctx, c, clients, &preflight.RouteVerifier{Clients: clients, Settlements: backends})
}
