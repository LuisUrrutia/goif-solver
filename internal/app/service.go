package app

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/solver"
	"go.uber.org/zap"
)

type Service struct {
	*solver.Service
	Handler http.Handler
}

func New(ctx context.Context, c config.Config, node string, execute bool, log *zap.Logger) (*Service, error) {
	return NewWithFactories(ctx, c, node, execute, log, builtins())
}

func NewWithFactories(ctx context.Context, c config.Config, node string, execute bool, log *zap.Logger, factories map[intent.Kind]Factory) (*Service, error) {
	if node == "" || len(node) > 128 {
		return nil, errors.New("node ID required")
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	store, closeStore, err := OpenStore(c)
	if err != nil {
		return nil, err
	}
	runtime, err := Open(ctx, c, store, execute, log, factories)
	if err != nil {
		closeStore()
		return nil, err
	}
	service := &Service{Service: &solver.Service{Execute: execute, Engine: &solver.Engine{Store: store, Executors: map[intent.Kind]solver.Executor{}}, Log: log, Node: node, Workers: c.Workers, Interval: time.Duration(c.WorkIntervalSeconds) * time.Second, Shutdown: func() { runtime.Close(); closeStore() }}}
	success := false
	defer func() {
		if !success {
			service.Close()
		}
	}()
	if err = store.Ping(ctx); err != nil {
		return nil, errors.Join(errors.New("coordination backend unavailable"), err)
	}
	if err = runtime.Bind(ctx, store, c); err != nil {
		return nil, err
	}
	for kind, execution := range runtime.Executions {
		service.Engine.Executors[kind] = execution.Executor
	}
	service.Sources = runtime.Sources
	quoter, err := runtime.quoter(c, store, execute)
	if err != nil {
		return nil, err
	}
	if quoter != nil {
		service.Quotes = quoter
	}
	service.Handler, err = runtime.httpHandler(c, service.Service, execute)
	if err != nil {
		return nil, err
	}
	success = true
	return service, nil
}
