// Package coordination defines work coordination independent of its storage backend.
package coordination

import (
	"errors"

	"github.com/LuisUrrutia/goif-solver/internal/intent"
)

var (
	ErrNotFound  = errors.New("record not found")
	ErrLeaseLost = errors.New("lease lost")
	ErrConflict  = errors.New("immutable record conflict")
	ErrBusy      = errors.New("resource busy")
)

const (
	intentResourcePrefix = "order:"
	signerResourcePrefix = "signer:"
	QuoteResource        = "quotes"
)

// Keep the persisted prefix stable for existing transaction journals.
func IntentResource(id string) string { return intentResourcePrefix + id }

type Lease struct {
	Resource string
	Token    int64
}
type Record struct {
	ID      string       `json:"id"`
	Payload string       `json:"payload"`
	Stage   intent.Stage `json:"stage"`
	Detail  string       `json:"detail"`
}

// Transaction is immutable after preparation. The signed bytes are persisted
// before any network broadcast and reused after uncertain RPC outcomes.
type Transaction struct {
	Operation string
	Raw       string
	Hash      string
	Nonce     uint64
}
