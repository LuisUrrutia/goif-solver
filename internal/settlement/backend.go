// Package settlement defines resumable verification without prescribing a VM,
// proof format, remote job API, or delivery mechanism.
package settlement

import (
	"context"
	"encoding/json"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
)

type ID string

type Evidence struct {
	Kind    intent.Kind     `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}

type Request struct {
	Evidence Evidence
	IntentID string
	Lease    coordination.Lease
}

type Status uint8

const (
	Pending Status = iota + 1
	Verified
)

// State belongs to the adapter and must be persisted before the next Advance.
// A retry repeats the same request: signed effects must use the fenced journal.
type Result struct {
	State      json.RawMessage
	RetryAfter time.Duration
	Status     Status
}

type Verification struct {
	Reference string `json:"reference,omitempty"`
	Verified  bool   `json:"verified"`
}

// Each backend is bound to one configured route. Verify must reject incompatible
// oracles before any fill; Inspect confirms the result without causing effects.
type Backend interface {
	Verify(context.Context) error
	Advance(context.Context, Request, json.RawMessage) (Result, error)
	Inspect(context.Context, Evidence) (Verification, error)
}

// AccessChecker is optional: an authenticated diagnostic, never a transaction.
type AccessChecker interface {
	CheckAccess(context.Context, Evidence) error
}
