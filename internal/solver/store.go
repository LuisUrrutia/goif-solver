package solver

import (
	"context"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
)

type Store interface {
	coordination.Leases
	Ping(context.Context) error
	Enqueue(context.Context, string, string) (bool, error)
	Record(context.Context, string) (coordination.Record, error)
	Ready(context.Context, int64, int64) ([]string, error)
	Stats(context.Context) (coordination.QueueStats, error)
	Advance(context.Context, coordination.Lease, string, intent.Stage, intent.Stage, string, bool, time.Duration) error
	Control(context.Context) (coordination.Control, error)
	SetControl(context.Context, uint64, coordination.Control) error
}
