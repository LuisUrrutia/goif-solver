package app

import (
	"context"
	"errors"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/preflight"
	"go.uber.org/zap"
)

func AuditIntent(ctx context.Context, c config.Config, key, provider string, access bool) (preflight.IntentReport, error) {
	identity, err := intent.ParseIdentity(key)
	if err != nil {
		return preflight.IntentReport{}, err
	}
	runtime, err := Open(ctx, c, nil, false, zap.NewNop(), builtins())
	if err != nil {
		return preflight.IntentReport{}, err
	}
	defer runtime.Close()
	if provider != "" {
		definition, ok := c.Providers[provider]
		if !ok {
			return preflight.IntentReport{}, errors.New("unknown history provider")
		}
		selected, err := openProvider(c, definition, zap.NewNop())
		if err != nil {
			return preflight.IntentReport{}, err
		}
		return selected.history(ctx, identity, access)
	}
	if c.Storage.Kind == config.MemoryStorage {
		return preflight.IntentReport{}, errors.New("memory history belongs to the running process")
	}
	store, closeStore, err := OpenStore(c)
	if err != nil {
		return preflight.IntentReport{}, err
	}
	defer closeStore()
	record, err := store.Record(ctx, key)
	if err != nil {
		return preflight.IntentReport{}, err
	}
	return runtime.AuditRecord(ctx, record, identity, access)
}

func (r *Runtime) AuditRecord(ctx context.Context, record coordination.Record, identity intent.Identity, access bool) (preflight.IntentReport, error) {
	if record.ID != identity.Key() {
		return preflight.IntentReport{}, errors.New("durable intent identity mismatch")
	}
	execution := r.Executions[identity.Kind]
	if execution == nil || execution.Audit == nil {
		return preflight.IntentReport{}, errors.New("intent inspection adapter unavailable")
	}
	return execution.Audit(ctx, record, access)
}
