// Package escrow defines EVM escrow intents, route policies, and contract bindings.
package escrow

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/quote"

	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"github.com/ethereum/go-ethereum/common"
)

// These ABI tuple types retain positional field order for DecodeOpen conversion.
type StandardOrder struct {
	User          common.Address
	Nonce         *big.Int
	OriginChainId *big.Int
	Expires       uint32
	FillDeadline  uint32
	InputOracle   common.Address
	Inputs        [][2]*big.Int
	Outputs       []Output
}
type Output struct {
	Oracle       [32]byte
	Settler      [32]byte
	ChainId      *big.Int
	Token        [32]byte
	Amount       *big.Int
	Recipient    [32]byte
	CallbackData []byte
	Context      []byte
}
type Route struct {
	InputSymbol      string                `json:"input_symbol,omitempty"`
	OutputSymbol     string                `json:"output_symbol,omitempty"`
	Settlement       settlement.ID         `json:"settlement"`
	Name             string                `json:"name"`
	Signer           string                `json:"signer"`
	MaxInput         string                `json:"max_input"`
	MaxOutput        string                `json:"max_output"`
	Pricing          quote.PricingSettings `json:"pricing"`
	OriginChain      uint64                `json:"origin_chain"`
	DestinationChain uint64                `json:"destination_chain"`
	InputSettler     common.Address        `json:"input_settler"`
	OutputSettler    common.Address        `json:"output_settler"`
	InputOracle      common.Address        `json:"input_oracle"`
	OutputOracle     common.Address        `json:"output_oracle"`
	InputToken       common.Address        `json:"input_token"`
	OutputToken      common.Address        `json:"output_token"`
	DeadlineBuffer   uint32                `json:"deadline_buffer_seconds"`
	InputDecimals    uint8                 `json:"input_decimals"`
	OutputDecimals   uint8                 `json:"output_decimals"`
}
type Validated struct {
	Order StandardOrder
	Route Route
	ID    common.Hash
}

type ParsedIntent struct {
	Order        StandardOrder
	ID           common.Hash
	InputSettler common.Address
}

func ParseIntent(w IntentData) (ParsedIntent, error) {
	o, err := Parse(w.Order)
	if err != nil {
		return ParsedIntent{}, err
	}
	id, err := evm.Word(w.ID)
	if err != nil {
		return ParsedIntent{}, err
	}
	settler, err := evm.Address(w.InputSettler)
	if err != nil {
		return ParsedIntent{}, err
	}
	return ParsedIntent{Order: o, ID: common.Hash(id), InputSettler: settler}, nil
}

func Parse(w OrderData) (StandardOrder, error) {
	var o StandardOrder
	var e error
	if o.User, e = evm.Address(w.User); e != nil {
		return o, e
	}
	if o.InputOracle, e = evm.Address(w.InputOracle); e != nil {
		return o, e
	}
	if o.Nonce, e = evm.Uint(w.Nonce, 256); e != nil {
		return o, e
	}
	if o.OriginChainId, e = evm.Uint(w.OriginChainID, 64); e != nil {
		return o, e
	}
	expiry, e := evm.Uint(w.Expires, 32)
	if e != nil {
		return o, e
	}
	deadline, e := evm.Uint(w.FillDeadline, 32)
	if e != nil {
		return o, e
	}
	o.Expires = uint32(expiry.Uint64())        // #nosec G115 -- evm.Uint(..., 32) above rejects negative values and values wider than 32 bits.
	o.FillDeadline = uint32(deadline.Uint64()) // #nosec G115 -- evm.Uint(..., 32) above enforces the uint32 bound.
	if len(w.Inputs) != 1 || len(w.Outputs) != 1 {
		return o, errors.New("only one input and one output supported")
	}
	for _, in := range w.Inputs {
		if len(in) != 2 {
			return o, errors.New("invalid input tuple")
		}
		token, e := evm.Uint(in[0], 160)
		if e != nil {
			return o, e
		}
		amount, e := evm.Uint(in[1], 256)
		if e != nil || amount.Sign() <= 0 {
			return o, errors.New("invalid input amount")
		}
		o.Inputs = append(o.Inputs, [2]*big.Int{token, amount})
	}
	for _, v := range w.Outputs {
		var out Output
		if out.Oracle, e = evm.Word(v.Oracle); e != nil {
			return o, e
		}
		if out.Settler, e = evm.Word(v.Settler); e != nil {
			return o, e
		}
		if out.Token, e = evm.Word(v.Token); e != nil {
			return o, e
		}
		if out.Recipient, e = evm.Word(v.Recipient); e != nil {
			return o, e
		}
		if out.ChainId, e = evm.Uint(v.ChainID, 64); e != nil {
			return o, e
		}
		if out.Amount, e = evm.Uint(v.Amount, 256); e != nil || out.Amount.Sign() <= 0 {
			return o, errors.New("invalid output amount")
		}
		if v.CallbackData != "0x" {
			return o, errors.New("callbacks unsupported")
		}
		out.CallbackData = []byte{}
		if !strings.HasPrefix(v.Context, "0x") || len(v.Context) > 76 {
			return o, errors.New("unsupported context")
		}
		out.Context, e = hex.DecodeString(v.Context[2:])
		if e != nil {
			return o, errors.New("invalid context hex")
		}
		o.Outputs = append(o.Outputs, out)
	}
	return o, nil
}

// Validate checks immutable policy. Escrow identity, status, and available
// inventory must also be checked at a finalized block before spending.
func (p ParsedIntent) Validate(r Route, solver common.Address, now time.Time) (Validated, error) {
	o := p.Order
	if !o.MatchesRoute(p.InputSettler, r) {
		return Validated{}, errors.New("route identity mismatch")
	}
	out := o.Outputs[0]
	if out.Recipient == ([32]byte{}) || !bytes.Equal(out.Recipient[:12], make([]byte, 12)) {
		return Validated{}, errors.New("invalid EVM recipient")
	}
	if now.Unix() < 0 || now.Unix() >= int64(o.FillDeadline)-int64(r.DeadlineBuffer) || int64(o.Expires) < int64(o.FillDeadline)+3600 {
		return Validated{}, errors.New("unsafe order deadline")
	}
	pricing, err := quote.NewPricing(r.Pricing, r.MaxInput, r.MaxOutput, r.InputDecimals, r.OutputDecimals)
	if err != nil {
		return Validated{}, err
	}
	if err = quote.Admit(pricing, o.Inputs[0][1], out.Amount); err != nil {
		return Validated{}, err
	}
	c := out.Context
	switch {
	case len(c) == 0, len(c) == 1 && c[0] == 0:
	case len(c) == 37 && c[0] == 0xe0:
		var exclusive [32]byte
		copy(exclusive[:], c[1:33])
		until := binary.BigEndian.Uint32(c[33:])
		if now.Unix() < int64(until) && exclusive != evm.AddressWord(solver) {
			return Validated{}, errors.New("exclusive to another solver")
		}
	default:
		return Validated{}, errors.New("unsupported auction context")
	}
	return Validated{ID: p.ID, Order: o, Route: r}, nil
}

// MatchesRoute compares immutable identity without applying live admission policy.
func (o StandardOrder) MatchesRoute(inputSettler common.Address, r Route) bool {
	if inputSettler != r.InputSettler {
		return false
	}
	out := o.Outputs[0]
	return o.OriginChainId.Uint64() == r.OriginChain && out.ChainId.Uint64() == r.DestinationChain &&
		o.InputOracle == r.InputOracle && out.Oracle == evm.AddressWord(r.OutputOracle) &&
		out.Settler == evm.AddressWord(r.OutputSettler) && common.BigToAddress(o.Inputs[0][0]) == r.InputToken && out.Token == evm.AddressWord(r.OutputToken)
}

// Canonical removes mutable source metadata and normalizes equivalent encodings
// so separate discovery adapters agree on one immutable Redis payload.
func Canonical(v Validated) IntentData {
	o := v.Order
	w := IntentData{InputSettler: v.Route.InputSettler.Hex(), Order: OrderData{User: o.User.Hex(), Nonce: o.Nonce.String(), OriginChainID: o.OriginChainId.String(), Expires: strconv.FormatUint(uint64(o.Expires), 10), FillDeadline: strconv.FormatUint(uint64(o.FillDeadline), 10), InputOracle: o.InputOracle.Hex(), Inputs: [][]string{{o.Inputs[0][0].String(), o.Inputs[0][1].String()}}}}
	for _, out := range o.Outputs {
		w.Order.Outputs = append(w.Order.Outputs, OutputData{Oracle: common.Hash(out.Oracle).Hex(), Settler: common.Hash(out.Settler).Hex(), Token: common.Hash(out.Token).Hex(), Recipient: common.Hash(out.Recipient).Hex(), ChainID: out.ChainId.String(), Amount: out.Amount.String(), CallbackData: "0x" + hex.EncodeToString(out.CallbackData), Context: "0x" + hex.EncodeToString(out.Context)})
	}
	w.ID = v.ID.Hex()
	return w
}
