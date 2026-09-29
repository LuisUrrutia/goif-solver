// Package coordination defines work coordination independent of its storage backend.
package coordination

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/LuisUrrutia/goif-solver/internal/intent"
)

var (
	ErrNotFound      = errors.New("record not found")
	ErrLeaseLost     = errors.New("lease lost")
	ErrConflict      = errors.New("immutable record conflict")
	ErrBusy          = errors.New("resource busy")
	ErrUnsafeStorage = errors.New("coordination durability or primary identity changed; stop and reconcile before restarting")
)

const (
	intentResourcePrefix = "order:"
	signerResourcePrefix = "signer:"
	quoteResourcePrefix  = "quotes:"
	sourceResourcePrefix = "sources:"
)

// Keep the persisted prefix stable for existing transaction journals.
func IntentResource(id string) string       { return intentResourcePrefix + id }
func QuoteLease(binding string) string      { return quoteResourcePrefix + binding }
func SourceLease(id intent.SourceID) string { return sourceResourcePrefix + string(id) }

func SignerResource(network, account string) string {
	return signerResourcePrefix + url.PathEscape(network) + ":" + url.PathEscape(account)
}

func IsIntentResource(resource string) bool {
	return strings.HasPrefix(resource, intentResourcePrefix) && len(resource) > len(intentResourcePrefix)
}

func IsSignerResource(resource string) bool {
	return strings.HasPrefix(resource, signerResourcePrefix) && len(resource) > len(signerResourcePrefix)
}

type Lease struct {
	Resource string
	Token    int64
}
type Record struct {
	ID        string       `json:"id"`
	Payload   string       `json:"payload"`
	Stage     intent.Stage `json:"stage"`
	Detail    string       `json:"detail"`
	CreatedAt int64        `json:"created_at"`
	UpdatedAt int64        `json:"updated_at"`
}

// Transaction is immutable after preparation. The signed bytes are persisted
// before any network broadcast and reused after uncertain RPC outcomes.
type Transaction struct {
	Operation string
	Raw       string
	Hash      string
	Codec     string
	Metadata  string
}

func (t Transaction) Validate() error {
	if t.Operation == "" || t.Raw == "" || t.Hash == "" || t.Codec == "" || !json.Valid([]byte(t.Metadata)) {
		return errors.New("invalid immutable transaction attempt")
	}
	return nil
}

type TerminalState string

const (
	Finalized TerminalState = "finalized"
	Expired   TerminalState = "expired"
)

// Outcome is adapter-verified evidence that an attempt cannot execute again.
// Expiry requires protocol evidence, never a lease or wall-clock timeout alone.
type Outcome struct {
	State    TerminalState `json:"state"`
	Evidence string        `json:"evidence"`
}

func (o Outcome) Validate() error {
	if (o.State != Finalized && o.State != Expired) || !json.Valid([]byte(o.Evidence)) || o.Evidence == "null" {
		return errors.New("verified terminal outcome required")
	}
	return nil
}
