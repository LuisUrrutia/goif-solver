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
	api, err := lifi.New(c.OrderAPI, "", c.RequestsPerSecond)
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
