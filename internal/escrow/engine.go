// Package escrow implements the EVM escrow settlement workflow.
package escrow

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/polymer"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

const (
	Validated      intent.Stage = "validated"
	Approved       intent.Stage = "approved"
	Filled         intent.Stage = "filled"
	ProofRequested intent.Stage = "proof-requested"
	ProofReady     intent.Stage = "proof-ready"
	Proven         intent.Stage = "proven"
)

type operation string

const (
	fillOperation    operation = "fill"
	approveOperation operation = "approve"
	relayOperation   operation = "relay"
	claimOperation   operation = "claim"
)

type Work struct {
	Route    string         `json:"route"`
	Envelope evm.IntentData `json:"envelope"`
	Version  uint64         `json:"version"`
}
type Progress struct {
	Fill               *evm.FillEvent `json:"fill,omitempty"`
	OriginBalance      string         `json:"origin_balance,omitempty"`
	DestinationBalance string         `json:"destination_balance,omitempty"`
	Proof              []byte         `json:"proof,omitempty"`
	Job                uint64         `json:"job,omitempty"`
}
type RouteVerifier interface {
	Verify(context.Context, evm.Route) error
}
type Engine struct {
	Verifier RouteVerifier
	Config   config.Config
	Store    coordination.Backend
	Clients  map[uint64]*ethclient.Client
	Senders  map[string]map[uint64]*evm.Sender
	Proofs   *polymer.Client
	Execute  bool
}

type execution struct {
	e           *Engine
	ctx         context.Context
	lease       coordination.Lease
	record      coordination.Record
	work        *Work
	address     common.Address
	v           evm.Validated
	progress    *Progress
	durable     *intent.Progress
	origin      *ethclient.Client
	destination *ethclient.Client
	senders     map[uint64]*evm.Sender
}

var settlementSteps = map[intent.Stage]func(*execution) error{
	Validated:      (*execution).onValidated,
	Approved:       (*execution).onApproved,
	Filled:         (*execution).onFilled,
	ProofRequested: (*execution).onProofRequested,
	ProofReady:     (*execution).onProofReady,
	Proven:         (*execution).onProven,
	intent.Settled: (*execution).onSettled,
}

func (e *Engine) Step(ctx context.Context, lease coordination.Lease, record coordination.Record) error {
	var work Work
	if err := json.Unmarshal([]byte(record.Payload), &work); err != nil {
		return errors.New("corrupt intent payload")
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
	id, err := evm.Word(work.Envelope.ID)
	if err != nil {
		return err
	}
	v := evm.Validated{ID: common.Hash(id), Order: o, Route: route}
	if !e.Config.AllowsIntent(v.ID) {
		return errors.Join(intent.ErrRejected, errors.New("order outside configured allowlist"))
	}
	if record.ID != v.ID.Hex() {
		return errors.New("order key differs from payload")
	}
	var progress Progress
	var durable intent.Progress
	if record.Detail != "" {
		if err = json.Unmarshal([]byte(record.Detail), &durable); err != nil {
			return errors.New("corrupt intent progress")
		}
	}
	if len(durable.State) > 0 {
		if err = json.Unmarshal(durable.State, &progress); err != nil {
			return errors.New("corrupt settlement state")
		}
	}
	origin, destination := e.Clients[route.OriginChain], e.Clients[route.DestinationChain]
	x := execution{e: e, ctx: ctx, lease: lease, record: record, work: &work, address: address, v: v, progress: &progress, durable: &durable, origin: origin, destination: destination, senders: e.Senders[route.Signer]}
	if record.Stage == intent.Discovered {
		if err := x.validate(); err != nil {
			return err
		}
		return x.advance(Validated, false)
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
		return intent.ErrObserve
	}
	if e.Verifier == nil {
		return errors.New("route verifier unavailable")
	}
	if err := e.Verifier.Verify(ctx, route); err != nil {
		return err
	}
	if x.senders == nil {
		return errors.New("route signer unavailable")
	}
	handler, ok := settlementSteps[record.Stage]
	if !ok {
		return errors.New("unknown settlement stage")
	}
	err = handler(&x)
	if errors.Is(err, evm.ErrPending) || errors.Is(err, polymer.ErrPending) || errors.Is(err, coordination.ErrBusy) {
		return &intent.Deferred{Cause: err, After: 2 * time.Second}
	}
	return err
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
	hash, err := evm.PayloadHash(v.ID, fill.Solver, fill.Timestamp, o)
	if err != nil {
		return false, err
	}
	values, err := evm.Call(ctx, client, v.Route.InputOracle, evm.OracleABI, nil, "isProven", o.ChainId, o.Oracle, o.Settler, hash)
	if err != nil {
		return false, err
	}
	return values[0].(bool), nil
}

func (e *Engine) Prepare(candidate intent.Candidate) (intent.Candidate, error) {
	var envelope evm.IntentData
	if json.Unmarshal(candidate.Payload, &envelope) != nil || candidate.ID != envelope.ID {
		return intent.Candidate{}, intent.ErrRejected
	}
	for _, route := range e.Config.Routes {
		var signer common.Address
		for _, definition := range e.Config.Signers {
			if definition.Name == route.Signer {
				signer = definition.Address
			}
		}
		validated, err := evm.Validate(envelope, route, signer, time.Now())
		if err != nil || !e.Config.AllowsIntent(validated.ID) {
			continue
		}
		payload, err := json.Marshal(Work{Version: e.Config.Version, Route: route.Name, Envelope: evm.Canonical(validated)})
		if err != nil {
			return intent.Candidate{}, err
		}
		return intent.Candidate{ID: validated.ID.Hex(), Kind: evm.IntentKind, Payload: payload}, nil
	}
	return intent.Candidate{}, intent.ErrRejected
}

func (e *Engine) Recover(ctx context.Context) error {
	if !e.Execute {
		return nil
	}
	for _, signers := range e.Senders {
		for _, sender := range signers {
			err := sender.Recover(ctx)
			if err != nil && !errors.Is(err, coordination.ErrBusy) && !errors.Is(err, evm.ErrPending) {
				return err
			}
		}
	}
	return nil
}
