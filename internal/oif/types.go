// Package oif implements the pinned OIF HTTP wire contract independently of a VM.
package oif

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
)

type (
	OrderType       string
	IntentType      string
	SwapType        string
	SubmissionMode  string
	FailureHandling string
)

const (
	UserOpen       OrderType       = "oif-user-open-v0"
	SwapIntent     IntentType      = "oif-swap"
	ExactInput     SwapType        = "exact-input"
	UserSubmission SubmissionMode  = "user"
	RefundClaim    FailureHandling = "refund-claim"
)

var ErrUnsupported = errors.New("unsupported OIF request")

type Address struct {
	Chain   string `json:"chain"`
	Address string `json:"address"`
}
type Input struct {
	Chain  string          `json:"chain"`
	User   string          `json:"user"`
	Asset  string          `json:"asset"`
	Amount string          `json:"amount,omitempty"`
	Lock   json.RawMessage `json:"lock,omitempty"`
}
type Output struct {
	Chain    string `json:"chain"`
	Receiver string `json:"receiver"`
	Asset    string `json:"asset"`
	Amount   string `json:"amount,omitempty"`
	Calldata string `json:"calldata,omitempty"`
}
type OriginSubmission struct {
	Mode    SubmissionMode `json:"mode"`
	Schemes []string       `json:"schemes,omitempty"`
}
type Swap struct {
	OriginSubmission *OriginSubmission          `json:"originSubmission,omitempty"`
	Metadata         map[string]json.RawMessage `json:"metadata,omitempty"`
	IntentType       IntentType                 `json:"intentType"`
	SwapType         SwapType                   `json:"swapType,omitempty"`
	Preference       string                     `json:"preference,omitempty"`
	Inputs           []Input                    `json:"inputs"`
	Outputs          []Output                   `json:"outputs"`
	FailureHandling  []FailureHandling          `json:"failureHandling,omitempty"`
	MinValidUntil    float64                    `json:"minValidUntil,omitempty"`
	PartialFill      bool                       `json:"partialFill,omitempty"`
}
type QuoteRequest struct {
	User           Address     `json:"user"`
	SupportedTypes []OrderType `json:"supportedTypes"`
	Intent         Swap        `json:"intent"`
}

// Bytes uses the spec's JSON number array; encoding/json's []byte is base64.
type Bytes []byte

func (b Bytes) MarshalJSON() ([]byte, error) {
	values := make([]uint16, len(b))
	for i, v := range b {
		values[i] = uint16(v)
	}
	return json.Marshal(values)
}

func (b *Bytes) UnmarshalJSON(raw []byte) error {
	var values []uint16
	if err := json.Unmarshal(raw, &values); err != nil {
		return ErrUnsupported
	}
	if values == nil || len(values) > 64<<10 {
		return ErrUnsupported
	}
	result := make([]byte, len(values))
	for i, v := range values {
		if v > 255 {
			return ErrUnsupported
		}
		result[i] = byte(v)
	}
	*b = result
	return nil
}

type OpenTransaction struct {
	Chain       string `json:"chain"`
	To          string `json:"to"`
	GasRequired string `json:"gasRequired"`
	Data        Bytes  `json:"data"`
}
type Allowance struct {
	Chain    string `json:"chain"`
	Token    string `json:"token"`
	User     string `json:"user"`
	Spender  string `json:"spender"`
	Required string `json:"required"`
}
type Checks struct {
	Allowances []Allowance `json:"allowances"`
}
type Order struct {
	Type         OrderType       `json:"type"`
	OpenIntentTx OpenTransaction `json:"openIntentTx"`
	Checks       Checks          `json:"checks"`
}
type Preview struct {
	Inputs  []Input  `json:"inputs"`
	Outputs []Output `json:"outputs"`
}
type Quote struct {
	QuoteID         string          `json:"quoteId,omitempty"`
	Provider        string          `json:"provider,omitempty"`
	FailureHandling FailureHandling `json:"failureHandling"`
	Order           Order           `json:"order"`
	Preview         Preview         `json:"preview"`
	ValidUntil      int64           `json:"validUntil"`
	PartialFill     bool            `json:"partialFill"`
}
type QuoteResponse struct {
	Quotes []Quote `json:"quotes"`
}
type Submission struct {
	OriginSubmission *OriginSubmission `json:"originSubmission,omitempty"`
	QuoteID          string            `json:"quoteId,omitempty"`
	Order            Order             `json:"order"`
	Signature        Bytes             `json:"signature,omitempty"`
}
type SubmissionStatus string

const (
	Received        SubmissionStatus = "received"
	Rejected        SubmissionStatus = "rejected"
	SubmissionError SubmissionStatus = "error"
)

type SubmissionResponse struct {
	OrderID string           `json:"orderId,omitempty"`
	Status  SubmissionStatus `json:"status"`
	Message string           `json:"message,omitempty"`
}
type Status string

const (
	Created   Status = "created"
	Pending   Status = "pending"
	Executing Status = "executing"
	Executed  Status = "executed"
	Settled   Status = "settled"
	Finalized Status = "finalized"
	Failed    Status = "failed"
)

type Amount struct {
	Asset  Address `json:"asset"`
	Amount string  `json:"amount"`
}
type Settlement struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}
type OrderResponse struct {
	ID              string          `json:"id"`
	Status          Status          `json:"status"`
	InputAmounts    []Amount        `json:"inputAmounts"`
	OutputAmounts   []Amount        `json:"outputAmounts"`
	Settlement      Settlement      `json:"settlement"`
	FillTransaction json.RawMessage `json:"fillTransaction,omitempty"`
	CreatedAt       int64           `json:"createdAt"`
	UpdatedAt       int64           `json:"updatedAt"`
}
type Asset struct {
	Address  string `json:"address"`
	Symbol   string `json:"symbol"`
	Decimals uint8  `json:"decimals"`
}
type Network struct {
	Assets  []Asset `json:"assets"`
	ChainID uint64  `json:"chain_id"`
}
type AssetsResponse struct {
	Networks map[string]Network `json:"networks"`
}

type Route interface {
	Quote(context.Context, QuoteRequest) (Quote, error)
	Decode(Order) (intent.Candidate, error)
	Status(coordination.Record) (OrderResponse, error)
	Assets() map[string]Network
}

func UserSubmitted(preference *OriginSubmission) bool {
	return preference == nil || preference.Mode == UserSubmission && len(preference.Schemes) == 0
}
