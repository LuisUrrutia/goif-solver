package app

import (
	"context"
	"errors"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/preflight"
	evmpreflight "github.com/LuisUrrutia/goif-solver/internal/preflight/evm"
	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"github.com/ethereum/go-ethereum/ethclient"
)

func auditEscrow(ctx context.Context, c config.Config, d escrowprotocol.Deployment, envelope escrowprotocol.IntentData, status, fillTx string, clients map[uint64]*ethclient.Client, access bool) (preflight.IntentReport, error) {
	backends, err := configureSettlements(c, d, clients, nil, false)
	if err != nil {
		return preflight.IntentReport{}, err
	}
	report, err := evmpreflight.AuditIntent(ctx, d, envelope, status, fillTx, clients, backends)
	if err != nil || !access {
		return report, err
	}
	scoped := d
	scoped.Routes = nil
	for _, route := range d.Routes {
		if route.Name == report.Route {
			scoped.Routes = append(scoped.Routes, route)
		}
	}
	backends, err = configureSettlements(c, scoped, clients, nil, true)
	if err != nil {
		return report, err
	}
	checker, ok := backends[report.Route].(settlement.AccessChecker)
	if !ok {
		return report, errors.New("settlement backend has no access diagnostic")
	}
	return report, checker.CheckAccess(ctx, report.Evidence)
}
