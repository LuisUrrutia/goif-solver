// Package escrow implements the EVM escrow settlement workflow.
package escrow

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"sync"
	"time"

	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

const (
	Validated intent.Stage = "validated"
	Approved  intent.Stage = "approved"
	Filled    intent.Stage = "filled"
	Proven    intent.Stage = "proven"
)

type operation string

const (
	fillOperation    operation = "fill"
	approveOperation operation = "approve"
	claimOperation   operation = "claim"
)

type Work struct {
	Settlement settlement.ID             `json:"settlement"`
	Route      string                    `json:"route"`
	Envelope   escrowprotocol.IntentData `json:"envelope"`
	Version    uint64                    `json:"version"`
}
type Progress struct {
	Fill               *escrowprotocol.FillEvent `json:"fill,omitempty"`
	OriginBalance      string                    `json:"origin_balance,omitempty"`
	DestinationBalance string                    `json:"destination_balance,omitempty"`
	Settlement         json.RawMessage           `json:"settlement,omitempty"`
}
type RouteVerifier interface {
	Verify(context.Context, escrowprotocol.Route) error
}
type Policy struct {
	escrowprotocol.Deployment
	IntentAllowlist []intent.Identity
	Version         uint64
}

func (c Policy) AllowsIntent(id common.Hash) bool {
	if len(c.IntentAllowlist) == 0 {
		return true
	}
	for _, allowed := range c.IntentAllowlist {
		if allowed.Kind == escrowprotocol.IntentKind && allowed.NativeID == id.Hex() {
			return true
		}
	}
	return false
}

type StateStore interface {
	Advance(context.Context, coordination.Lease, string, intent.Stage, intent.Stage, string, bool, time.Duration) error
	Record(context.Context, string) (coordination.Record, error)
	Transaction(context.Context, string, string) (coordination.Transaction, error)
}
type Engine struct {
	Verifier    RouteVerifier
	Config      Policy
	Store       StateStore
	Clients     map[uint64]*ethclient.Client
	Senders     map[string]map[uint64]*evm.Sender
	Settlements map[string]settlement.Backend
	Execute     bool
}

type execution struct {
	e           *Engine
	ctx         context.Context
	lease       coordination.Lease
	record      coordination.Record
	work        *Work
	address     common.Address
	v           escrowprotocol.Validated
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
	var route escrowprotocol.Route
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
	if work.Settlement == "" || work.Settlement != route.Settlement {
		return errors.New("intent settlement binding changed")
	}
	var address common.Address
	for _, s := range e.Config.Signers {
		if s.Name == route.Signer {
			address = s.Address
		}
	}
	o, err := escrowprotocol.Parse(work.Envelope.Order)
	if err != nil {
		return err
	}
	id, err := evm.Word(work.Envelope.ID)
	if err != nil {
		return err
	}
	v := escrowprotocol.Validated{ID: common.Hash(id), Order: o, Route: route}
	if !e.Config.AllowsIntent(v.ID) {
		return errors.Join(intent.ErrRejected, errors.New("order outside configured allowlist"))
	}
	if record.ID != (intent.Identity{Kind: escrowprotocol.IntentKind, NativeID: v.ID.Hex()}).Key() {
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
	if errors.Is(err, evm.ErrPending) || errors.Is(err, coordination.ErrBusy) {
		return &intent.Deferred{Cause: err, After: 2 * time.Second}
	}
	return err
}

func (e *Engine) Prepare(candidate intent.Candidate) (intent.Candidate, error) {
	var envelope escrowprotocol.IntentData
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
		validated, err := escrowprotocol.Validate(envelope, route, signer, time.Now())
		if err != nil || !e.Config.AllowsIntent(validated.ID) {
			continue
		}
		payload, err := json.Marshal(Work{Settlement: route.Settlement, Version: e.Config.Version, Route: route.Name, Envelope: escrowprotocol.Canonical(validated)})
		if err != nil {
			return intent.Candidate{}, err
		}
		return intent.Candidate{ID: validated.ID.Hex(), Kind: escrowprotocol.IntentKind, Payload: payload}, nil
	}
	return intent.Candidate{}, intent.ErrRejected
}

func (e *Engine) Recover(ctx context.Context) error {
	if !e.Execute {
		return nil
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []error
	for _, signers := range e.Senders {
		for _, sender := range signers {
			wg.Go(func() {
				attempt, cancel := context.WithTimeout(ctx, 20*time.Second)
				defer cancel()
				err := sender.Recover(attempt)
				if err != nil && !errors.Is(err, coordination.ErrBusy) && !errors.Is(err, evm.ErrPending) {
					mu.Lock()
					failures = append(failures, err)
					mu.Unlock()
				}
			})
		}
	}
	wg.Wait()
	return errors.Join(failures...)
}
