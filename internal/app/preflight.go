package app

import (
	"context"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	"github.com/LuisUrrutia/goif-solver/internal/preflight"
)

func Preflight(ctx context.Context, c config.Config) (preflight.Report, error) {
	api, err := lifi.New(c.OrderAPI, "", c.RequestsPerSecond)
	if err != nil {
		return preflight.Report{}, err
	}
	if err = api.CheckCatalog(ctx, c.Routes); err != nil {
		return preflight.Report{}, err
	}
	return preflight.Run(ctx, c)
}
