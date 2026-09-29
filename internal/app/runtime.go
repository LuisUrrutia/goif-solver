package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/preflight"
	"github.com/LuisUrrutia/goif-solver/internal/quote"
	"github.com/LuisUrrutia/goif-solver/internal/solver"
	"go.uber.org/zap"
)

type Execution struct {
	Executor solver.Executor
	Checker  preflight.Checker
	Quotes   map[string]quote.Source
	Source   func(config.Source) (intent.Source, error)
	Audit    func(context.Context, coordination.Record, bool) (preflight.IntentReport, error)
	Policy   json.RawMessage
	Close    func()
}
type (
	Factory func(context.Context, config.Config, json.RawMessage, coordination.Backend, bool, *zap.Logger) (*Execution, error)
	Runtime struct {
		Executions map[intent.Kind]*Execution
		Providers  map[string]providerSet
		Sources    []intent.Source
	}
)

func (r *Runtime) Close() {
	for _, execution := range r.Executions {
		execution.Close()
	}
}

func Open(ctx context.Context, c config.Config, store coordination.Backend, execute bool, log *zap.Logger, factories map[intent.Kind]Factory) (*Runtime, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if execute && (c.Development || store == nil) {
		return nil, errors.New("execution requires durable coordination")
	}
	runtime := &Runtime{Executions: map[intent.Kind]*Execution{}}
	success := false
	defer func() {
		if !success {
			runtime.Close()
		}
	}()
	for kind, settings := range c.Executions {
		factory := factories[kind]
		if factory == nil {
			return nil, fmt.Errorf("execution adapter %s is not installed", kind)
		}
		execution, err := factory(ctx, c, settings, store, execute, log)
		if err != nil {
			return nil, err
		}
		runtime.Executions[kind] = execution
	}
	providers, err := configureProviders(c, runtime.Executions)
	if err != nil {
		return nil, err
	}
	runtime.Providers = providers
	runtime.Sources, err = runtime.sources(c)
	if err != nil {
		return nil, err
	}
	success = true
	return runtime, nil
}

func (r *Runtime) Bind(ctx context.Context, store coordination.Backend, c config.Config) error {
	policies := map[intent.Kind]json.RawMessage{}
	for kind, execution := range r.Executions {
		policies[kind] = execution.Policy
	}
	allowlist := slices.Clone(c.IntentAllowlist)
	slices.SortFunc(allowlist, func(a, b intent.Identity) int {
		if a.Key() < b.Key() {
			return -1
		}
		if a.Key() > b.Key() {
			return 1
		}
		return 0
	})
	raw, err := json.Marshal(struct {
		Version    uint64                          `json:"version"`
		Allowlist  []intent.Identity               `json:"allowlist"`
		Executions map[intent.Kind]json.RawMessage `json:"executions"`
	}{c.Version, allowlist, policies})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	if err = store.BindConfig(ctx, hex.EncodeToString(digest[:])); err != nil {
		return errors.New("fleet execution policy differs; drain and migrate namespace")
	}
	return nil
}
