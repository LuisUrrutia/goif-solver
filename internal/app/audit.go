package app

import (
	"context"
	"errors"
	"strings"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	"github.com/LuisUrrutia/goif-solver/internal/preflight"
)

func AuditIntent(ctx context.Context, c config.Config, id string) (preflight.IntentReport, error) {
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
	return preflight.AuditIntent(ctx, c, envelope.Intent(), envelope.Meta.Status, envelope.Meta.FillTx)
}
