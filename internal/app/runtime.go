package app

import (
	"bytes"
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
	"github.com/LuisUrrutia/goif-solver/internal/oif"
	"github.com/LuisUrrutia/goif-solver/internal/preflight"
	"github.com/LuisUrrutia/goif-solver/internal/quote"
	"github.com/LuisUrrutia/goif-solver/internal/solver"
	"go.uber.org/zap"
)

type Execution struct {
	Executor solver.Executor
	Checker  preflight.Checker
	OIF      map[string]oif.Route
	Quotes   map[string]quote.Source
	Source   func(config.Source) (intent.Source, error)
	Audit    func(context.Context, coordination.Record, bool) (preflight.IntentReport, error)
	Close    func()
	Policy   json.RawMessage
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
	providers, err := configureProviders(c, runtime.Executions, log)
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
		if !json.Valid(execution.Policy) {
			return errors.New("invalid execution policy encoding")
		}
		var policy any
		decoder := json.NewDecoder(bytes.NewReader(execution.Policy))
		decoder.UseNumber()
		if err := decoder.Decode(&policy); err != nil {
			return errors.New("invalid execution policy encoding")
		}
		raw, err := json.Marshal(policy)
		if err != nil {
			return err
		}
		policies[kind] = raw
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
	raw, err := json.Marshal(map[string]any{"version": c.Version, "allowlist": allowlist, "executions": policies})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	if err = store.BindConfig(ctx, hex.EncodeToString(digest[:])); err != nil {
		return errors.New("fleet execution policy differs; drain and migrate namespace")
	}
	return nil
}
