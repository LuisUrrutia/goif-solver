package app

import (
	"context"
	"errors"
	"strings"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	"github.com/LuisUrrutia/goif-solver/internal/preflight"
	evmpreflight "github.com/LuisUrrutia/goif-solver/internal/preflight/evm"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
)

func AuditIntent(ctx context.Context, c config.Config, id string) (preflight.IntentReport, error) {
	return auditIntent(ctx, c, id, false)
}

func CheckProofAccess(ctx context.Context, c config.Config, id string) (preflight.IntentReport, error) {
	return auditIntent(ctx, c, id, true)
}

func auditIntent(ctx context.Context, c config.Config, id string, access bool) (preflight.IntentReport, error) {
	if c.Providers.LIFI == nil {
		return preflight.IntentReport{}, errors.New("historical API audit requires LI.FI provider")
	}
	api, err := lifi.New(c.Providers.LIFI.API, "", c.RequestsPerSecond)
	if err != nil {
		return preflight.IntentReport{}, err
	}
	envelope, err := api.Order(ctx, id)
	if err != nil {
		return preflight.IntentReport{}, err
	}
	if !strings.EqualFold(envelope.Meta.ID, id) {
		return preflight.IntentReport{}, errors.New("order API returned another identifier")
	}
	clients, closeClients, err := openClients(ctx, c)
	if err != nil {
		return preflight.IntentReport{}, err
	}
	defer closeClients()
	backends, err := configureSettlements(c, clients, nil, false)
	if err != nil {
		return preflight.IntentReport{}, err
	}
	report, err := evmpreflight.AuditIntent(ctx, c, envelope.Intent(), envelope.Meta.Status, envelope.Meta.FillTx, clients, backends)
	if err != nil || !access {
		return report, err
	}
	scoped := c
	scoped.Routes = nil
	for _, route := range c.Routes {
		if route.Name == report.Route {
			scoped.Routes = append(scoped.Routes, route)
		}
	}
	backends, err = configureSettlements(scoped, clients, nil, true)
	if err != nil {
		return report, err
	}
	checker, ok := backends[report.Route].(settlement.AccessChecker)
	if !ok {
		return report, errors.New("settlement backend has no access diagnostic")
	}
	return report, checker.CheckAccess(ctx, report.Evidence)
}
