// Package escrow adapts the deployed escrow protocol to OIF user-open requests.
package escrow

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"slices"
	"strconv"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	workflow "github.com/LuisUrrutia/goif-solver/internal/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/oif"
	protocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/quote"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

type Route struct {
	Source quote.Source
	Verify func(context.Context, protocol.Route) error
	Policy protocol.Route
	Signer common.Address
	Gas    uint64
}

func chain(id uint64) string { return "eip155:" + strconv.FormatUint(id, 10) }
func (r *Route) Quote(ctx context.Context, request oif.QuoteRequest) (oif.Quote, error) {
	swap := request.Intent
	if !slices.Contains(request.SupportedTypes, oif.UserOpen) || swap.IntentType != oif.SwapIntent || swap.SwapType != "" && swap.SwapType != oif.ExactInput || swap.PartialFill || !oif.UserSubmitted(swap.OriginSubmission) || len(swap.Inputs) != 1 || len(swap.Outputs) != 1 || len(swap.FailureHandling) > 0 && !slices.Contains(swap.FailureHandling, oif.RefundClaim) {
		return oif.Quote{}, oif.ErrUnsupported
	}
	switch swap.Preference {
	case "", "price", "speed", "input-priority", "trust-minimization":
	default:
		return oif.Quote{}, oif.ErrUnsupported
	}
	input, output := swap.Inputs[0], swap.Outputs[0]
	if len(input.Lock) != 0 || output.Calldata != "" && output.Calldata != "0x" || input.Chain != chain(r.Policy.OriginChain) || output.Chain != chain(r.Policy.DestinationChain) || request.User.Chain != input.Chain {
		return oif.Quote{}, oif.ErrUnsupported
	}
	user, err := evm.Address(input.User)
	if err != nil {
		return oif.Quote{}, oif.ErrUnsupported
	}
	owner, err := evm.Address(request.User.Address)
	if err != nil || owner != user {
		return oif.Quote{}, oif.ErrUnsupported
	}
	receiver, err := evm.Address(output.Receiver)
	if err != nil {
		return oif.Quote{}, oif.ErrUnsupported
	}
	token, err := evm.Address(input.Asset)
	if err != nil || token != r.Policy.InputToken {
		return oif.Quote{}, oif.ErrUnsupported
	}
	token, err = evm.Address(output.Asset)
	if err != nil || token != r.Policy.OutputToken {
		return oif.Quote{}, oif.ErrUnsupported
	}
	amount, err := evm.Uint(input.Amount, 256)
	if err != nil {
		return oif.Quote{}, oif.ErrUnsupported
	}
	pricing, err := quote.NewPricing(r.Policy.Pricing, r.Policy.MaxInput, r.Policy.MaxOutput, r.Policy.InputDecimals, r.Policy.OutputDecimals)
	if err != nil {
		return oif.Quote{}, err
	}
	out, err := pricing.Output(amount)
	if err != nil {
		return oif.Quote{}, oif.ErrUnsupported
	}
	if output.Amount != "" {
		minimum, err := evm.Uint(output.Amount, 256)
		if err != nil || minimum.Cmp(out) > 0 {
			return oif.Quote{}, oif.ErrUnsupported
		}
	}
	now := time.Now()
	validUntil := now.Add(time.Minute).Unix()
	if swap.MinValidUntil > float64(validUntil) {
		return oif.Quote{}, oif.ErrUnsupported
	}
	if err = r.Verify(ctx, r.Policy); err != nil {
		return oif.Quote{}, err
	}
	offer, err := r.Source.Offer(ctx, false)
	if err != nil {
		return oif.Quote{}, err
	}
	if len(offer.Ranges) == 0 {
		return oif.Quote{}, errors.New("route inventory unavailable")
	}
	nonce, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 256))
	if err != nil {
		return oif.Quote{}, err
	}
	deadline := now.Add(10 * time.Minute).Unix()
	if deadline+3600 > math.MaxUint32 || deadline <= int64(r.Policy.DeadlineBuffer)+validUntil {
		return oif.Quote{}, errors.New("route deadline cannot cover quote validity")
	}
	order := protocol.StandardOrder{User: user, Nonce: nonce, OriginChainId: new(big.Int).SetUint64(r.Policy.OriginChain), FillDeadline: uint32(deadline), Expires: uint32(deadline + 3600), InputOracle: r.Policy.InputOracle, Inputs: [][2]*big.Int{{new(big.Int).SetBytes(r.Policy.InputToken[:]), amount}}, Outputs: []protocol.Output{{Oracle: evm.AddressWord(r.Policy.OutputOracle), Settler: evm.AddressWord(r.Policy.OutputSettler), ChainId: new(big.Int).SetUint64(r.Policy.DestinationChain), Token: evm.AddressWord(r.Policy.OutputToken), Amount: out, Recipient: evm.AddressWord(receiver)}}} // #nosec G115 -- The bounded positive Unix deadline and expiry fit uint32 above.
	wire, err := r.wire(order)
	if err != nil {
		return oif.Quote{}, err
	}
	input.Amount = amount.String()
	output.Amount = out.String()
	return oif.Quote{Order: wire, ValidUntil: validUntil, Preview: oif.Preview{Inputs: []oif.Input{input}, Outputs: []oif.Output{output}}, FailureHandling: oif.RefundClaim}, nil
}

func (r *Route) wire(order protocol.StandardOrder) (oif.Order, error) {
	data, err := protocol.InputABI.Pack("open", order)
	if err != nil {
		return oif.Order{}, err
	}
	return oif.Order{Type: oif.UserOpen, OpenIntentTx: oif.OpenTransaction{Chain: chain(r.Policy.OriginChain), To: r.Policy.InputSettler.Hex(), Data: data, GasRequired: strconv.FormatUint(r.Gas, 10)}, Checks: oif.Checks{Allowances: []oif.Allowance{{Chain: chain(r.Policy.OriginChain), Token: r.Policy.InputToken.Hex(), User: order.User.Hex(), Spender: r.Policy.InputSettler.Hex(), Required: order.Inputs[0][1].String()}}}}, nil
}

func (r *Route) Decode(wire oif.Order) (intent.Candidate, error) {
	tx := wire.OpenIntentTx
	if wire.Type != oif.UserOpen || tx.Chain != chain(r.Policy.OriginChain) || len(tx.Data) < 4 || !bytes.Equal(tx.Data[:4], protocol.InputABI.Methods["open"].ID) {
		return intent.Candidate{}, oif.ErrUnsupported
	}
	to, err := evm.Address(tx.To)
	if err != nil || to != r.Policy.InputSettler {
		return intent.Candidate{}, oif.ErrUnsupported
	}
	values, err := protocol.InputABI.Methods["open"].Inputs.Unpack(tx.Data[4:])
	if err != nil || len(values) != 1 {
		return intent.Candidate{}, oif.ErrUnsupported
	}
	order := *abi.ConvertType(values[0], new(protocol.StandardOrder)).(*protocol.StandardOrder)
	if len(order.Inputs) != 1 || len(order.Outputs) != 1 {
		return intent.Candidate{}, oif.ErrUnsupported
	}
	id, err := protocol.Identifier(order, r.Policy.InputSettler)
	if err != nil {
		return intent.Candidate{}, oif.ErrUnsupported
	}
	envelope := protocol.Canonical(protocol.Validated{ID: id, Order: order, Route: r.Policy})
	if _, err = protocol.Validate(envelope, r.Policy, r.Signer, time.Now()); err != nil {
		return intent.Candidate{}, oif.ErrUnsupported
	}
	expected, err := r.wire(order)
	if err != nil {
		return intent.Candidate{}, oif.ErrUnsupported
	}
	actualJSON, err := json.Marshal(wire)
	if err != nil {
		return intent.Candidate{}, err
	}
	expectedJSON, err := json.Marshal(expected)
	if err != nil || !bytes.Equal(actualJSON, expectedJSON) {
		return intent.Candidate{}, oif.ErrUnsupported
	}
	raw, err := json.Marshal(envelope)
	return intent.Candidate{Kind: protocol.IntentKind, ID: id.Hex(), Payload: raw}, err
}

var stages = map[intent.Stage]oif.Status{intent.Discovered: oif.Created, workflow.Validated: oif.Pending, workflow.Approved: oif.Executing, workflow.Filled: oif.Executed, workflow.Proven: oif.Settled, intent.Settled: oif.Finalized, intent.Rejected: oif.Failed}

func (r *Route) Status(record coordination.Record) (oif.OrderResponse, error) {
	var candidate intent.Candidate
	var work workflow.Work
	if json.Unmarshal([]byte(record.Payload), &candidate) != nil || candidate.Kind != protocol.IntentKind || candidate.Identity().Key() != record.ID || json.Unmarshal(candidate.Payload, &work) != nil || work.Route != r.Policy.Name {
		return oif.OrderResponse{}, oif.ErrUnsupported
	}
	order, err := protocol.Parse(work.Envelope.Order)
	if err != nil || !protocol.MatchesRoute(order, work.Envelope.InputSettler, r.Policy) {
		return oif.OrderResponse{}, oif.ErrUnsupported
	}
	state, ok := stages[record.Stage]
	if !ok {
		return oif.OrderResponse{}, errors.New("unknown durable escrow stage")
	}
	settlement, err := json.Marshal(struct {
		InputSettler string `json:"inputSettler"`
		Protocol     string `json:"protocol"`
	}{InputSettler: r.Policy.InputSettler.Hex(), Protocol: string(protocol.IntentKind)})
	if err != nil {
		return oif.OrderResponse{}, err
	}
	response := oif.OrderResponse{Status: state, InputAmounts: []oif.Amount{{Asset: oif.Address{Chain: chain(r.Policy.OriginChain), Address: r.Policy.InputToken.Hex()}, Amount: order.Inputs[0][1].String()}}, OutputAmounts: []oif.Amount{{Asset: oif.Address{Chain: chain(r.Policy.DestinationChain), Address: r.Policy.OutputToken.Hex()}, Amount: order.Outputs[0].Amount.String()}}, Settlement: oif.Settlement{Type: "escrow", Data: settlement}}
	if record.Detail != "" {
		var progress intent.Progress
		var state workflow.Progress
		if json.Unmarshal([]byte(record.Detail), &progress) != nil || len(progress.State) > 0 && json.Unmarshal(progress.State, &state) != nil {
			return oif.OrderResponse{}, errors.New("invalid intent progress")
		}
		if state.Fill != nil {
			response.FillTransaction, err = json.Marshal(struct {
				Chain string `json:"chain"`
				Hash  string `json:"hash"`
			}{Chain: chain(r.Policy.DestinationChain), Hash: state.Fill.Log.TxHash.Hex()})
		}
	}
	return response, err
}

func (r *Route) Assets() map[string]oif.Network {
	return map[string]oif.Network{
		strconv.FormatUint(r.Policy.OriginChain, 10):      {ChainID: r.Policy.OriginChain, Assets: []oif.Asset{{Address: interoperable(r.Policy.OriginChain, r.Policy.InputToken), Symbol: r.Policy.InputSymbol, Decimals: r.Policy.InputDecimals}}},
		strconv.FormatUint(r.Policy.DestinationChain, 10): {ChainID: r.Policy.DestinationChain, Assets: []oif.Asset{{Address: interoperable(r.Policy.DestinationChain, r.Policy.OutputToken), Symbol: r.Policy.OutputSymbol, Decimals: r.Policy.OutputDecimals}}},
	}
}

func interoperable(chain uint64, address common.Address) string {
	reference := new(big.Int).SetUint64(chain).Bytes()
	result := []byte{0, 1, 0, 0, byte(len(reference))} // #nosec G115 -- uint64 references use at most eight bytes.
	result = append(result, reference...)
	result = append(result, 20)
	result = append(result, address[:]...)
	return "0x" + hex.EncodeToString(result)
}
