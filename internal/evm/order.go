// Package evm implements configured EVM execution and StandardOrder validation.
package evm

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
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
	Name             string         `json:"name"`
	Signer           string         `json:"signer"`
	MaxInput         string         `json:"max_input"`
	MaxOutput        string         `json:"max_output"`
	MinMargin        string         `json:"min_margin"`
	OriginChain      uint64         `json:"origin_chain"`
	DestinationChain uint64         `json:"destination_chain"`
	InputSettler     common.Address `json:"input_settler"`
	OutputSettler    common.Address `json:"output_settler"`
	InputOracle      common.Address `json:"input_oracle"`
	OutputOracle     common.Address `json:"output_oracle"`
	InputToken       common.Address `json:"input_token"`
	OutputToken      common.Address `json:"output_token"`
	DeadlineBuffer   uint32         `json:"deadline_buffer_seconds"`
	InputDecimals    uint8          `json:"input_decimals"`
	OutputDecimals   uint8          `json:"output_decimals"`
}
type Validated struct {
	Order StandardOrder
	Route Route
	ID    common.Hash
}

func Uint(s string, bits int) (*big.Int, error) {
	if s == "" || len(s) > 78 || len(s) > 1 && s[0] == '0' {
		return nil, errors.New("noncanonical unsigned integer")
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return nil, errors.New("invalid unsigned integer")
		}
	}
	n, ok := new(big.Int).SetString(s, 10)
	if !ok || n.BitLen() > bits {
		return nil, errors.New("unsigned integer overflow")
	}
	return n, nil
}
func Address(s string) (common.Address, error) {
	if len(s) != 42 || !strings.HasPrefix(s, "0x") || !common.IsHexAddress(s) {
		return common.Address{}, errors.New("invalid EVM address")
	}
	a := common.HexToAddress(s)
	if a == (common.Address{}) {
		return a, errors.New("zero EVM address")
	}
	return a, nil
}
func Word(s string) ([32]byte, error) {
	var w [32]byte
	if len(s) != 66 || !strings.HasPrefix(s, "0x") {
		return w, errors.New("invalid bytes32")
	}
	b, e := hex.DecodeString(s[2:])
	if e != nil {
		return w, errors.New("invalid bytes32 hex")
	}
	copy(w[:], b)
	return w, nil
}
func AddressWord(a common.Address) [32]byte { var w [32]byte; copy(w[12:], a[:]); return w }
func Parse(w OrderData) (StandardOrder, error) {
	var o StandardOrder
	var e error
	if o.User, e = Address(w.User); e != nil {
		return o, e
	}
	if o.InputOracle, e = Address(w.InputOracle); e != nil {
		return o, e
	}
	if o.Nonce, e = Uint(w.Nonce, 256); e != nil {
		return o, e
	}
	if o.OriginChainId, e = Uint(w.OriginChainID, 64); e != nil {
		return o, e
	}
	expiry, e := Uint(w.Expires, 32)
	if e != nil {
		return o, e
	}
	deadline, e := Uint(w.FillDeadline, 32)
	if e != nil {
		return o, e
	}
	o.Expires = uint32(expiry.Uint64())
	o.FillDeadline = uint32(deadline.Uint64())
	if len(w.Inputs) != 1 || len(w.Outputs) != 1 {
		return o, errors.New("only one input and one output supported")
	}
	for _, in := range w.Inputs {
		if len(in) != 2 {
			return o, errors.New("invalid input tuple")
		}
		token, e := Uint(in[0], 160)
		if e != nil {
			return o, e
		}
		amount, e := Uint(in[1], 256)
		if e != nil || amount.Sign() <= 0 {
			return o, errors.New("invalid input amount")
		}
		o.Inputs = append(o.Inputs, [2]*big.Int{token, amount})
	}
	for _, v := range w.Outputs {
		var out Output
		if out.Oracle, e = Word(v.Oracle); e != nil {
			return o, e
		}
		if out.Settler, e = Word(v.Settler); e != nil {
			return o, e
		}
		if out.Token, e = Word(v.Token); e != nil {
			return o, e
		}
		if out.Recipient, e = Word(v.Recipient); e != nil {
			return o, e
		}
		if out.ChainId, e = Uint(v.ChainID, 64); e != nil {
			return o, e
		}
		if out.Amount, e = Uint(v.Amount, 256); e != nil || out.Amount.Sign() <= 0 {
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
func Validate(w IntentData, r Route, solver common.Address, now time.Time) (Validated, error) {
	o, e := Parse(w.Order)
	if e != nil {
		return Validated{}, e
	}
	id, e := Word(w.ID)
	if e != nil {
		return Validated{}, e
	}
	settler, e := Address(w.InputSettler)
	if e != nil || settler != r.InputSettler {
		return Validated{}, errors.New("input settler rejected")
	}
	out := o.Outputs[0]
	if o.OriginChainId.Uint64() != r.OriginChain || out.ChainId.Uint64() != r.DestinationChain || o.InputOracle != r.InputOracle || out.Oracle != AddressWord(r.OutputOracle) || out.Settler != AddressWord(r.OutputSettler) {
		return Validated{}, errors.New("route contract mismatch")
	}
	if common.BigToAddress(o.Inputs[0][0]) != r.InputToken || out.Token != AddressWord(r.OutputToken) {
		return Validated{}, errors.New("token mismatch")
	}
	if out.Recipient == ([32]byte{}) || !bytes.Equal(out.Recipient[:12], make([]byte, 12)) {
		return Validated{}, errors.New("invalid EVM recipient")
	}
	if uint64(o.FillDeadline) <= uint64(now.Unix())+uint64(r.DeadlineBuffer) || uint64(o.Expires) < uint64(o.FillDeadline)+3600 {
		return Validated{}, errors.New("unsafe order deadline")
	}
	for _, limit := range []struct {
		value *big.Int
		cap   string
	}{{o.Inputs[0][1], r.MaxInput}, {out.Amount, r.MaxOutput}} {
		cap, e := Uint(limit.cap, 256)
		if e != nil || cap.Sign() <= 0 || limit.value.Cmp(cap) > 0 {
			return Validated{}, errors.New("amount exceeds route cap")
		}
	}
	margin, e := Uint(r.MinMargin, 256)
	if e != nil {
		return Validated{}, e
	}
	if new(big.Int).Sub(o.Inputs[0][1], out.Amount).Cmp(margin) < 0 {
		return Validated{}, errors.New("insufficient route margin")
	}
	c := out.Context
	switch {
	case len(c) == 0, len(c) == 1 && c[0] == 0:
	case len(c) == 37 && c[0] == 0xe0:
		var exclusive [32]byte
		copy(exclusive[:], c[1:33])
		until := binary.BigEndian.Uint32(c[33:])
		if uint64(now.Unix()) < uint64(until) && exclusive != AddressWord(solver) {
			return Validated{}, errors.New("exclusive to another solver")
		}
	default:
		return Validated{}, errors.New("unsupported auction context")
	}
	return Validated{ID: common.Hash(id), Order: o, Route: r}, nil
}
func SignerResource(chain uint64, address common.Address) string {
	return "signer:" + strconv.FormatUint(chain, 10) + ":" + strings.ToLower(address.Hex())
}
func PayloadHash(id common.Hash, solver [32]byte, timestamp uint32, o Output) common.Hash {
	b := append([]byte{0xd1, 0x25, 0x2d, 0xff}, solver[:]...)
	b = append(b, id[:]...)
	b = binary.BigEndian.AppendUint32(b, timestamp)
	b = append(b, o.Token[:]...)
	b = append(b, common.LeftPadBytes(o.Amount.Bytes(), 32)...)
	b = append(b, o.Recipient[:]...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(o.CallbackData)))
	b = append(b, o.CallbackData...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(o.Context)))
	b = append(b, o.Context...)
	return crypto.Keccak256Hash(b)
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
