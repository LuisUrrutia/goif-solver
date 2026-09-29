package escrow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"time"

	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"github.com/LuisUrrutia/goif-solver/internal/transport"
	"github.com/ethereum/go-ethereum/common"
)

var errAllowanceRequired = errors.New("fill requires a new token approval")

func (x *execution) onValidated() error {
	if err := x.validate(); err != nil {
		return err
	}
	if err := x.reserveAllowance(); err != nil {
		return err
	}
	balance, err := evm.Balance(x.ctx, x.destination, x.v.Route.OutputToken, x.address)
	if err != nil {
		return err
	}
	if balance.Cmp(x.v.Order.Outputs[0].Amount) < 0 {
		return errors.New("insufficient destination inventory")
	}
	allowance, err := evm.Call(x.ctx, x.destination, x.v.Route.OutputToken, evm.TokenABI, nil, "allowance", x.address, x.v.Route.OutputSettler)
	if err != nil {
		return err
	}
	if allowance[0].(*big.Int).Cmp(x.v.Order.Outputs[0].Amount) < 0 {
		data, err := evm.TokenABI.Pack("approve", x.v.Route.OutputSettler, x.v.Order.Outputs[0].Amount)
		if err != nil {
			return err
		}
		if _, err = x.send(x.v.Route.DestinationChain, approveOperation, x.v.Route.OutputToken, data); err != nil {
			return err
		}
	}
	return x.advance(Approved, false)
}

func (x *execution) onApproved() error {
	if err := x.reserveAllowance(); err != nil {
		return err
	}
	_, journalErr := x.e.Store.Transaction(x.ctx, evm.SignerResource(x.v.Route.DestinationChain, x.address), x.record.ID+":"+string(fillOperation))
	if errors.Is(journalErr, coordination.ErrNotFound) {
		if err := x.validate(); err != nil {
			return err
		}
		filled, err := evm.Call(x.ctx, x.destination, x.v.Route.OutputSettler, escrowprotocol.OutputABI, nil, "getFillRecord", x.v.ID, x.v.Order.Outputs[0])
		if err != nil {
			return err
		}
		if filled[0].([32]byte) != ([32]byte{}) {
			return errors.New("destination already filled without local journal; reconcile manually")
		}
	} else if journalErr != nil {
		return journalErr
	}
	solver := evm.AddressWord(x.address)
	data, err := escrowprotocol.OutputABI.Pack("fillOrderOutputs", x.v.ID, x.v.Order.Outputs, new(big.Int).SetUint64(uint64(x.v.Order.FillDeadline)), solver[:])
	if err != nil {
		return err
	}
	fill, err := x.send(x.v.Route.DestinationChain, fillOperation, x.v.Route.OutputSettler, data)
	if errors.Is(err, errAllowanceRequired) {
		x.progress.ApprovalAttempt++
		return x.advance(Validated, false)
	}
	if err != nil {
		return err
	}
	x.progress.Fill = fill
	return x.advance(Filled, false)
}

func (x *execution) onFilled() error {
	if x.progress.Fill == nil {
		return errors.New("missing fill coordinates")
	}
	backend := x.e.Settlements[x.work.Route]
	if backend == nil {
		return errors.New("settlement backend unavailable")
	}
	evidence, err := escrowprotocol.SettlementEvidence(x.v, *x.progress.Fill)
	if err != nil {
		return err
	}
	result, err := backend.Advance(x.ctx, settlement.Request{IntentID: x.record.ID, Lease: x.lease, Evidence: evidence}, x.progress.Settlement)
	if err != nil {
		return err
	}
	if result.RetryAfter < 0 {
		return errors.New("negative settlement retry delay")
	}
	switch result.Status {
	case settlement.Pending:
		x.progress.Settlement = result.State
		return x.advanceAfter(Filled, false, result.RetryAfter)
	case settlement.Verified:
		verification, err := backend.Inspect(x.ctx, evidence)
		if err != nil {
			return err
		}
		if !verification.Verified {
			return errors.New("settlement backend did not verify fulfillment")
		}
		x.progress.Settlement = result.State
		return x.advance(Proven, false)
	default:
		return errors.New("invalid settlement result")
	}
}

func (x *execution) onProven() error {
	if x.progress.Fill == nil {
		return errors.New("missing fill")
	}
	params := []struct {
		Timestamp uint32
		Solver    [32]byte
	}{{x.progress.Fill.Timestamp, x.progress.Fill.Solver}}
	data, err := escrowprotocol.InputABI.Pack("finalise", x.v.Order, params, evm.AddressWord(x.address), []byte{})
	if err != nil {
		return err
	}
	if _, err = x.send(x.v.Route.OriginChain, claimOperation, x.v.Route.InputSettler, data); err != nil {
		return err
	}
	status, err := escrowprotocol.OrderStatus(x.ctx, x.origin, x.v, nil)
	if err != nil {
		return err
	}
	if status != escrowprotocol.EscrowClaimed {
		return errors.New("claim receipt did not settle escrow")
	}
	originBalance, err := evm.Balance(x.ctx, x.origin, x.v.Route.InputToken, x.address)
	if err != nil {
		return err
	}
	destinationBalance, err := evm.Balance(x.ctx, x.destination, x.v.Route.OutputToken, x.address)
	if err != nil {
		return err
	}
	x.progress.OriginBalance = originBalance.String()
	x.progress.DestinationBalance = destinationBalance.String()
	return x.advance(intent.Settled, true)
}

func (x *execution) onSettled() error {
	return nil
}

func (x *execution) advance(stage intent.Stage, terminal bool) error {
	return x.advanceAfter(stage, terminal, 0)
}

func (x *execution) advanceAfter(stage intent.Stage, terminal bool, delay time.Duration) error {
	x.durable.Attempts = 0
	x.durable.LastError = ""
	b, err := json.Marshal(x.progress)
	if err != nil {
		return err
	}
	x.durable.State = b
	b, err = json.Marshal(x.durable)
	if err != nil {
		return err
	}
	return x.e.Store.Advance(x.ctx, x.lease, x.record.ID, x.record.Stage, stage, string(b), terminal, delay)
}

func (x *execution) validate() error {
	if _, err := escrowprotocol.Validate(x.work.Envelope, x.v.Route, x.address, time.Now()); err != nil {
		return errors.Join(intent.ErrRejected, err)
	}
	if err := escrowprotocol.ZeroGovernanceFee(x.ctx, x.origin, x.v.Route.InputSettler); err != nil {
		return err
	}
	confirmations := uint64(0)
	for _, c := range x.e.Config.Chains {
		if c.ID == x.v.Route.OriginChain {
			confirmations = c.Confirmations
		}
	}
	head, err := x.origin.BlockNumber(x.ctx)
	if err != nil {
		return transport.Failure(x.ctx, "query origin finality", err)
	}
	if head < confirmations {
		return errors.New("origin finality unavailable")
	}
	confirmed, err := escrowprotocol.OrderStatus(x.ctx, x.origin, x.v, new(big.Int).SetUint64(head-confirmations))
	if err != nil {
		return err
	}
	latest, err := escrowprotocol.OrderStatus(x.ctx, x.origin, x.v, nil)
	if err != nil {
		return err
	}
	for _, status := range []escrowprotocol.EscrowStatus{confirmed, latest} {
		if status == escrowprotocol.EscrowClaimed || status == escrowprotocol.EscrowRefunded {
			return errors.Join(intent.ErrRejected, errors.New("escrow is claimed or refunded"))
		}
	}
	if latest != escrowprotocol.EscrowDeposited {
		return errors.New("order is not deposited in escrow")
	}
	if confirmed != escrowprotocol.EscrowDeposited {
		return &intent.Deferred{Cause: errors.New("origin deposit awaiting finality"), After: 2 * time.Second}
	}
	return nil
}

func (x *execution) send(chain uint64, operation operation, to common.Address, data []byte) (*escrowprotocol.FillEvent, error) {
	request := evm.SendRequest{Operation: x.record.ID + ":" + string(operation), To: to, Data: data}
	if operation == approveOperation && x.progress.ApprovalAttempt > 0 {
		request.Operation += ":" + strconv.FormatUint(x.progress.ApprovalAttempt, 10)
	}
	if operation == fillOperation {
		request.Check = x.checkAllowance
	}
	receipt, err := x.senders[chain].Execute(x.ctx, x.lease, request)
	if err != nil {
		return nil, err
	}
	if operation == fillOperation {
		fill, err := escrowprotocol.DecodeFill(receipt, x.v, x.address)
		return &fill, err
	}
	return nil, nil
}

func (x *execution) checkAllowance(ctx context.Context) error {
	allowance, err := evm.Call(ctx, x.destination, x.v.Route.OutputToken, evm.TokenABI, nil, "allowance", x.address, x.v.Route.OutputSettler)
	if err != nil {
		return err
	}
	if allowance[0].(*big.Int).Cmp(x.v.Order.Outputs[0].Amount) < 0 {
		return errAllowanceRequired
	}
	return nil
}

func (x *execution) reserveAllowance() error {
	route := x.v.Route
	resource := fmt.Sprintf("escrow-allowance:%d:%s:%s:%s", route.DestinationChain, x.address.Hex(), route.OutputToken.Hex(), route.OutputSettler.Hex())
	return x.e.Store.Reserve(x.ctx, x.lease, resource, Filled)
}
