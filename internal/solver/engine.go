// Package solver advances durable orders through the escrow/Polymer lifecycle.
package solver

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	"github.com/LuisUrrutia/goif-solver/internal/polymer"
	"github.com/LuisUrrutia/goif-solver/internal/preflight"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

var ErrObserve = errors.New("observation mode: spending disabled")
var ErrRejected = errors.New("order rejected by route policy")

type Work struct {
	Version  uint64        `json:"version"`
	Route    string        `json:"route"`
	Envelope lifi.Envelope `json:"envelope"`
}
type Progress struct {
	LastError          string         `json:"last_error,omitempty"`
	Fill               *evm.FillEvent `json:"fill,omitempty"`
	Job                uint64         `json:"job,omitempty"`
	Proof              []byte         `json:"proof,omitempty"`
	OriginBalance      string         `json:"origin_balance,omitempty"`
	DestinationBalance string         `json:"destination_balance,omitempty"`
	Attempts           uint32         `json:"attempts,omitempty"`
}
type Engine struct {
	Config  config.Config
	Store   coordination.Backend
	Clients map[uint64]*ethclient.Client
	Senders map[string]map[uint64]*evm.Sender
	Proofs  *polymer.Client
	Execute bool
}

func (e *Engine) Step(ctx context.Context, lease coordination.Lease, record coordination.Record) error {
	var work Work
	if err := json.Unmarshal([]byte(record.Payload), &work); err != nil {
		return errors.New("corrupt order payload")
	}
	if work.Version != e.Config.Version {
		return errors.New("order requires a different configuration version")
	}
	var route evm.Route
	found := false
	for _, r := range e.Config.Routes {
		if r.Name == work.Route {
			route = r
			found = true
			break
		}
	}
	if !found {
		return errors.New("order route no longer configured")
	}
	var address common.Address
	for _, s := range e.Config.Signers {
		if s.Name == route.Signer {
			address = s.Address
		}
	}
	o, err := evm.Parse(work.Envelope.Order)
	if err != nil {
		return err
	}
	id, err := evm.Word(work.Envelope.Meta.ID)
	if err != nil {
		return err
	}
	v := evm.Validated{ID: common.Hash(id), Order: o, Route: route}
	if !e.Config.AllowsOrder(v.ID) {
		return errors.Join(ErrRejected, errors.New("order outside configured allowlist"))
	}
	if record.ID != v.ID.Hex() {
		return errors.New("order key differs from payload")
	}
	var progress Progress
	if record.Detail != "" {
		if err = json.Unmarshal([]byte(record.Detail), &progress); err != nil {
			return errors.New("corrupt order progress")
		}
	}
	advance := func(stage string, terminal bool) error {
		progress.Attempts = 0
		progress.LastError = ""
		b, err := json.Marshal(progress)
		if err != nil {
			return err
		}
		return e.Store.Advance(ctx, lease, record.ID, record.Stage, stage, string(b), terminal, 0)
	}
	origin, destination := e.Clients[route.OriginChain], e.Clients[route.DestinationChain]
	validate := func() error {
		if _, err := evm.Validate(work.Envelope, route, address, time.Now()); err != nil {
			return errors.Join(ErrRejected, err)
		}
		if err := preflight.ZeroGovernanceFee(ctx, origin, route.InputSettler); err != nil {
			return err
		}
		confirmations := uint64(0)
		for _, c := range e.Config.Chains {
			if c.ID == route.OriginChain {
				confirmations = c.Confirmations
			}
		}
		head, err := origin.BlockNumber(ctx)
		if err != nil || head < confirmations {
			return errors.New("origin finality unavailable")
		}
		for _, block := range []*big.Int{new(big.Int).SetUint64(head - confirmations), nil} {
			status, err := evm.OrderStatus(ctx, origin, v, block)
			if err != nil {
				return err
			}
			if status != 1 {
				if status == 2 || status == 3 {
					return errors.Join(ErrRejected, errors.New("escrow is claimed or refunded"))
				}
				return errors.New("order is not deposited in escrow")
			}
		}
		return nil
	}
	if record.Stage == "discovered" {
		if err := validate(); err != nil {
			return err
		}
		return advance("validated", false)
	}
	if progress.Fill != nil {
		header, err := destination.HeaderByNumber(ctx, new(big.Int).SetUint64(progress.Fill.Log.BlockNumber))
		if err != nil {
			return errors.New("fill block reconciliation unavailable")
		}
		if header.Hash() != progress.Fill.Log.BlockHash {
			return errors.New("confirmed fill reorganized; manual reconciliation required")
		}
	}
	if !e.Execute {
		return ErrObserve
	}
	senders := e.Senders[route.Signer]
	if senders == nil {
		return errors.New("route signer unavailable")
	}
	send := func(chain uint64, operation string, to common.Address, data []byte) (*evm.FillEvent, error) {
		receipt, err := senders[chain].Execute(ctx, lease, record.ID+":"+operation, to, data)
		if err != nil {
			return nil, err
		}
		if operation == "fill" {
			fill, err := evm.DecodeFill(receipt, v, address)
			return &fill, err
		}
		return nil, nil
	}
	switch record.Stage {
	case "validated":
		if err := validate(); err != nil {
			return err
		}
		balance, err := evm.Balance(ctx, destination, route.OutputToken, address)
		if err != nil {
			return err
		}
		if balance.Cmp(o.Outputs[0].Amount) < 0 {
			return errors.New("insufficient destination inventory")
		}
		allowance, err := evm.Call(ctx, destination, route.OutputToken, evm.TokenABI, nil, "allowance", address, route.OutputSettler)
		if err != nil {
			return err
		}
		if allowance[0].(*big.Int).Cmp(o.Outputs[0].Amount) < 0 {
			data, err := evm.TokenABI.Pack("approve", route.OutputSettler, o.Outputs[0].Amount)
			if err != nil {
				return err
			}
			if _, err = send(route.DestinationChain, "approve", route.OutputToken, data); err != nil {
				return err
			}
		}
		return advance("approved", false)
	case "approved":
		_, journalErr := e.Store.Transaction(ctx, evm.SignerResource(route.DestinationChain, address), record.ID+":fill")
		if errors.Is(journalErr, coordination.ErrNotFound) {
			if err := validate(); err != nil {
				return err
			}
			filled, err := evm.Call(ctx, destination, route.OutputSettler, evm.OutputABI, nil, "getFillRecord", v.ID, o.Outputs[0])
			if err != nil {
				return err
			}
			if filled[0].([32]byte) != ([32]byte{}) {
				return errors.New("destination already filled without local journal; reconcile manually")
			}
		} else if journalErr != nil {
			return journalErr
		}
		solver := evm.AddressWord(address)
		data, err := evm.OutputABI.Pack("fillOrderOutputs", v.ID, o.Outputs, new(big.Int).SetUint64(uint64(o.FillDeadline)), solver[:])
		if err != nil {
			return err
		}
		fill, err := send(route.DestinationChain, "fill", route.OutputSettler, data)
		if err != nil {
			return err
		}
		progress.Fill = fill
		return advance("filled", false)
	case "filled":
		if progress.Fill == nil {
			return errors.New("missing fill coordinates")
		}
		job, err := e.Proofs.Request(ctx, polymer.Log{ChainID: route.DestinationChain, BlockNumber: progress.Fill.Log.BlockNumber, Index: progress.Fill.Log.Index})
		if err != nil {
			return err
		}
		progress.Job = job
		return advance("proof-requested", false)
	case "proof-requested":
		if progress.Job == 0 {
			return errors.New("missing proof job")
		}
		proof, err := e.Proofs.Query(ctx, progress.Job)
		if err != nil {
			return err
		}
		progress.Proof = proof
		return advance("proof-ready", false)
	case "proof-ready":
		if progress.Fill == nil || len(progress.Proof) == 0 {
			return errors.New("missing proof or fill")
		}
		proven, err := isProven(ctx, origin, v, progress.Fill)
		if err != nil {
			return err
		}
		if !proven {
			data, err := packSignature(evm.OracleABI, "receiveMessage(bytes)", progress.Proof)
			if err != nil {
				return err
			}
			if _, err = send(route.OriginChain, "relay", route.InputOracle, data); err != nil {
				return err
			}
		}
		proven, err = isProven(ctx, origin, v, progress.Fill)
		if err != nil {
			return err
		}
		if !proven {
			return errors.New("relay did not prove output")
		}
		return advance("proven", false)
	case "proven":
		if progress.Fill == nil {
			return errors.New("missing fill")
		}
		params := []struct {
			Timestamp uint32
			Solver    [32]byte
		}{{progress.Fill.Timestamp, progress.Fill.Solver}}
		data, err := evm.InputABI.Pack("finalise", o, params, evm.AddressWord(address), []byte{})
		if err != nil {
			return err
		}
		if _, err = send(route.OriginChain, "claim", route.InputSettler, data); err != nil {
			return err
		}
		status, err := evm.OrderStatus(ctx, origin, v, nil)
		if err != nil {
			return err
		}
		if status != 2 {
			return errors.New("claim receipt did not settle escrow")
		}
		originBalance, err := evm.Balance(ctx, origin, route.InputToken, address)
		if err != nil {
			return err
		}
		destinationBalance, err := evm.Balance(ctx, destination, route.OutputToken, address)
		if err != nil {
			return err
		}
		progress.OriginBalance = originBalance.String()
		progress.DestinationBalance = destinationBalance.String()
		return advance("settled", true)
	case "settled":
		return nil
	default:
		return errors.New("unknown order stage")
	}
}
func packSignature(contract abi.ABI, signature string, args ...interface{}) ([]byte, error) {
	for name, method := range contract.Methods {
		if method.Sig == signature {
			return contract.Pack(name, args...)
		}
	}
	return nil, errors.New("contract signature absent")
}
func isProven(ctx context.Context, client *ethclient.Client, v evm.Validated, fill *evm.FillEvent) (bool, error) {
	o := v.Order.Outputs[0]
	hash := evm.PayloadHash(v.ID, fill.Solver, fill.Timestamp, o)
	values, err := evm.Call(ctx, client, v.Route.InputOracle, evm.OracleABI, nil, "isProven", o.ChainId, o.Oracle, o.Settler, hash)
	if err != nil {
		return false, err
	}
	return values[0].(bool), nil
}
