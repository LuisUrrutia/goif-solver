// Package intent defines discovery and durable execution contracts without a
// dependency on a network, order server, or settlement implementation.
package intent

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

type Kind string

type Stage string

const (
	Discovered Stage = "discovered"
	Settled    Stage = "settled"
	Rejected   Stage = "rejected"
)

var (
	ErrRejected = errors.New("intent rejected by policy")
	ErrObserve  = errors.New("observation mode: spending disabled")
)

// Payload is owned and validated by the selected settlement adapter. Sources
// emit candidates; Prepare canonicalizes them before durable deduplication.
type Candidate struct {
	ID      string          `json:"id"`
	Kind    Kind            `json:"kind"`
	Payload json.RawMessage `json:"payload"`
}
type (
	Emit     func(context.Context, Candidate) error
	SourceID string
)

// Run remains active until cancellation or a recoverable source error. Emit
// returns only after durable acceptance; it also supplies bounded backpressure.
type Source interface {
	Identity() SourceID
	Run(context.Context, Emit) error
}
type Progress struct {
	LastError string          `json:"last_error,omitempty"`
	State     json.RawMessage `json:"state,omitempty"`
	Attempts  uint32          `json:"attempts,omitempty"`
}

// Deferred is an expected asynchronous wait, not a failed attempt.
type Deferred struct {
	Cause error
	After time.Duration
}

func (d *Deferred) Error() string { return d.Cause.Error() }
func (d *Deferred) Unwrap() error { return d.Cause }
