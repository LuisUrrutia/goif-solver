package app

import (
	"context"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/preflight"
	"go.uber.org/zap"
)

func Preflight(ctx context.Context, c config.Config) (preflight.Report, error) {
	runtime, err := Open(ctx, c, nil, false, zap.NewNop(), builtins())
	if err != nil {
		return preflight.Report{}, err
	}
	defer runtime.Close()
	for _, provider := range runtime.Providers {
		if provider.catalog != nil {
			if err = provider.catalog(ctx); err != nil {
				return preflight.Report{}, err
			}
		}
	}
	checks := make([]preflight.Checker, 0, len(runtime.Executions))
	for _, execution := range runtime.Executions {
		checks = append(checks, execution.Checker)
	}
	return preflight.Run(ctx, checks)
}
