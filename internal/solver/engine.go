package solver

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
)

type Executor interface {
	Prepare(intent.Candidate) (intent.Candidate, error)
	Step(context.Context, coordination.Lease, coordination.Record) error
	Recover(context.Context) error
}
type Engine struct {
	Store     coordination.Backend
	Executors map[intent.Kind]Executor
}

func (e *Engine) Prepare(candidate intent.Candidate) (intent.Candidate, error) {
	executor, ok := e.Executors[candidate.Kind]
	if !ok {
		return intent.Candidate{}, intent.ErrRejected
	}
	return executor.Prepare(candidate)
}
func (e *Engine) Step(ctx context.Context, lease coordination.Lease, record coordination.Record) error {
	var candidate intent.Candidate
	if err := json.Unmarshal([]byte(record.Payload), &candidate); err != nil {
		return errors.New("corrupt intent payload")
	}
	if candidate.ID != record.ID {
		return errors.New("intent identity mismatch")
	}
	executor, ok := e.Executors[candidate.Kind]
	if !ok {
		return errors.New("settlement strategy unavailable")
	}
	record.Payload = string(candidate.Payload)
	return executor.Step(ctx, lease, record)
}
