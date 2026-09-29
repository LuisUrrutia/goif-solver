package coordination

import (
	"context"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/intent"
)

// Backend is the coordination contract. Memory backends retain state only for
// the lifetime of one process. Persistent backends support a fleet. Implementations must make
// fenced transitions and two-lease transaction preparation atomic; merely
// implementing these method signatures is insufficient for safe execution.
// Completed records, fence counters, journals, and signer reservations cannot
// expire independently or roll back while the fleet can still broadcast.
// Missing records return ErrNotFound.
type Backend interface {
	Ping(context.Context) error
	Checkpoint(context.Context, string) (string, error)
	CommitCheckpoint(context.Context, string, string, string) error
	Enqueue(context.Context, string, string) (bool, error)
	Record(context.Context, string) (Record, error)
	Ready(context.Context, int64, int64) ([]string, error)
	// Claim acquires an intent lease only if the queued intent is still due.
	Claim(context.Context, string, time.Duration) (Lease, error)
	Stats(context.Context) (QueueStats, error)
	Acquire(context.Context, string, time.Duration) (Lease, error)
	Renew(context.Context, Lease, time.Duration) error
	Release(context.Context, Lease) error
	// Reserve retains a resource for this intent across worker leases, until
	// Advance reaches the requested stage or terminates the intent.
	Reserve(context.Context, Lease, string, intent.Stage) error
	Advance(context.Context, Lease, string, intent.Stage, intent.Stage, string, bool, time.Duration) error
	Prepare(context.Context, Lease, Lease, Transaction) error
	Pending(context.Context, string) (string, error)
	Transaction(context.Context, string, string) (Transaction, error)
	CompleteTransaction(context.Context, Lease, string, Outcome) error
	TransactionOutcome(context.Context, string, string) (Outcome, error)
	Control(context.Context) (Control, error)
	SetControl(context.Context, uint64, Control) error
	BindConfig(context.Context, string) error
}
