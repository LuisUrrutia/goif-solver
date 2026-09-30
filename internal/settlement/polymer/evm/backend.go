// Package polymerevm verifies and relays EVM escrow fulfillment through Polymer.
package polymerevm

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"github.com/LuisUrrutia/goif-solver/internal/settlement/polymer"
	"github.com/LuisUrrutia/goif-solver/internal/transport"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

//go:embed oracle.json
var oracleJSON string

//go:embed provenance.json
var provenanceJSON []byte

var OracleABI = func() abi.ABI {
	contract, err := abi.JSON(strings.NewReader(oracleJSON))
	if err != nil {
		panic(err)
	}
	return contract
}()

var oracleRuntime = func() common.Hash {
	var provenance struct {
		Hash common.Hash `json:"runtime_keccak256"`
	}
	if err := json.Unmarshal(provenanceJSON, &provenance); err != nil {
		panic(err)
	}
	return provenance.Hash
}()

const (
	stateVersion         = 2
	retryInterval        = 2 * time.Second
	defaultMaxProofJobs  = 3
	maxProofJobs         = 100
	defaultProofJobDelay = 30 * time.Second
	maxProofJobDelay     = 5 * time.Minute
)

var ErrJobLimit = errors.New("polymer proof job limit reached")

type RetryPolicy struct {
	MaxJobs      int
	InitialDelay time.Duration
}

func (p RetryPolicy) delay(requests int) time.Duration {
	delay := p.InitialDelay
	for attempt := 1; attempt < requests && delay < maxProofJobDelay; attempt++ {
		delay = min(delay*2, maxProofJobDelay)
	}
	return delay
}

type checkpoint struct {
	LastFailure *polymer.JobError `json:"last_failure,omitempty"`
	Proof       []byte            `json:"proof,omitempty"`
	Job         uint64            `json:"job,omitempty"`
	Requests    int               `json:"requests"`
	Version     uint8             `json:"version"`
}

type Backend struct {
	clients map[uint64]*ethclient.Client
	sender  *evm.Sender
	proofs  *polymer.Client
	id      settlement.ID
	route   escrowprotocol.Route
	retry   RetryPolicy
	signer  common.Address
}

var (
	_ settlement.Backend       = (*Backend)(nil)
	_ settlement.AccessChecker = (*Backend)(nil)
)

func NewBackend(id settlement.ID, route escrowprotocol.Route, signer common.Address, clients map[uint64]*ethclient.Client, sender *evm.Sender, proofs *polymer.Client, retry RetryPolicy) (*Backend, error) {
	if id == "" || route.Settlement != id || clients[route.OriginChain] == nil || clients[route.DestinationChain] == nil || signer == (common.Address{}) {
		return nil, errors.New("incomplete Polymer route binding")
	}
	if retry.MaxJobs == 0 {
		retry.MaxJobs = defaultMaxProofJobs
	}
	if retry.InitialDelay == 0 {
		retry.InitialDelay = defaultProofJobDelay
	}
	if retry.MaxJobs < 1 || retry.MaxJobs > maxProofJobs || retry.InitialDelay < time.Second || retry.InitialDelay > maxProofJobDelay {
		return nil, errors.New("invalid Polymer proof retry policy")
	}
	return &Backend{id: id, route: route, signer: signer, clients: clients, sender: sender, proofs: proofs, retry: retry}, nil
}

func (b *Backend) Verify(ctx context.Context) error {
	for _, side := range []struct {
		chain  uint64
		oracle common.Address
	}{{b.route.OriginChain, b.route.InputOracle}, {b.route.DestinationChain, b.route.OutputOracle}} {
		code, err := b.clients[side.chain].CodeAt(ctx, side.oracle, nil)
		if err != nil {
			return transport.Failure(ctx, "query oracle runtime", err)
		}
		if crypto.Keccak256Hash(code) != oracleRuntime {
			return errors.New("oracle runtime is incompatible with Polymer")
		}
	}
	return nil
}

func (b *Backend) Inspect(ctx context.Context, evidence settlement.Evidence) (settlement.Verification, error) {
	v, fill, err := escrowprotocol.DecodeFulfillment(evidence, b.route, b.signer)
	if err != nil {
		return settlement.Verification{}, err
	}
	return b.inspect(ctx, v, fill)
}

func (b *Backend) inspect(ctx context.Context, v escrowprotocol.Validated, fill escrowprotocol.FillEvent) (settlement.Verification, error) {
	output := v.Order.Outputs[0]
	hash, err := PayloadHash(v.ID, fill.Solver, fill.Timestamp, output)
	if err != nil {
		return settlement.Verification{}, err
	}
	values, err := evm.Call(ctx, b.clients[b.route.OriginChain], b.route.InputOracle, OracleABI, nil, "isProven", output.ChainId, output.Oracle, output.Settler, hash)
	if err != nil {
		return settlement.Verification{}, err
	}
	return settlement.Verification{Reference: hash.Hex(), Verified: values[0].(bool)}, nil
}

func pending(state checkpoint, delay time.Duration) (settlement.Result, error) {
	data, err := json.Marshal(state)
	return settlement.Result{State: data, Status: settlement.Pending, RetryAfter: delay}, err
}

func (b *Backend) Advance(ctx context.Context, request settlement.Request, state json.RawMessage) (settlement.Result, error) {
	v, fill, err := escrowprotocol.DecodeFulfillment(request.Evidence, b.route, b.signer)
	if err != nil {
		return settlement.Result{}, err
	}
	if (intent.Identity{Kind: escrowprotocol.IntentKind, NativeID: v.ID.Hex()}).Key() != request.IntentID || request.Lease.Resource != coordination.IntentResource(request.IntentID) {
		return settlement.Result{}, errors.New("settlement lease does not match fulfillment")
	}
	saved := checkpoint{Version: stateVersion}
	if len(state) > 0 {
		saved = checkpoint{}
		if json.Unmarshal(state, &saved) != nil || saved.Job == 0 {
			return settlement.Result{}, errors.New("invalid Polymer checkpoint")
		}
		if saved.Version == 1 {
			saved.Version = stateVersion
			saved.Requests = 1
		}
		if saved.Version != stateVersion || saved.Requests < 1 || saved.Requests > maxProofJobs ||
			saved.LastFailure != nil && (saved.LastFailure.JobID == 0 || saved.LastFailure.JobID == saved.Job && len(saved.Proof) > 0) {
			return settlement.Result{}, errors.New("invalid Polymer checkpoint")
		}
	}
	verification, err := b.inspect(ctx, v, fill)
	if err != nil {
		return settlement.Result{}, err
	}
	if verification.Verified {
		return settlement.Result{State: state, Status: settlement.Verified}, nil
	}
	if len(saved.Proof) > 0 {
		if b.sender == nil {
			return settlement.Result{}, errors.New("settlement transaction sender unavailable")
		}
		data, err := packMessage(saved.Proof)
		if err != nil {
			return settlement.Result{}, err
		}
		if _, err = b.sender.Execute(ctx, request.Lease, evm.SendRequest{Operation: request.IntentID + ":settlement:" + string(b.id) + ":relay", To: b.route.InputOracle, Data: data}); err != nil {
			return settlement.Result{}, err
		}
		verification, err = b.inspect(ctx, v, fill)
		if err != nil {
			return settlement.Result{}, err
		}
		if !verification.Verified {
			return settlement.Result{}, errors.New("relay did not verify fulfillment")
		}
		return settlement.Result{State: state, Status: settlement.Verified}, nil
	}
	if b.proofs == nil {
		return settlement.Result{}, errors.New("polymer proof access is not enabled")
	}
	failed := saved.LastFailure != nil && saved.LastFailure.JobID == saved.Job
	if failed && saved.Requests >= b.retry.MaxJobs {
		return settlement.Result{}, fmt.Errorf("%w after %d accepted requests (last job %d); operator recovery required", ErrJobLimit, saved.Requests, saved.Job)
	}
	if saved.Job == 0 || failed {
		saved.Job, err = b.proofs.RequestEVM(ctx, polymer.EVMLog{ChainID: b.route.DestinationChain, BlockNumber: fill.Log.BlockNumber, Index: fill.Log.Index})
		if err != nil {
			return settlement.Result{}, err
		}
		saved.Requests++
		if saved.LastFailure != nil && saved.LastFailure.JobID == saved.Job {
			return pending(saved, b.retry.delay(saved.Requests))
		}
		return pending(saved, 0)
	}
	saved.Proof, err = b.proofs.Query(ctx, saved.Job)
	if errors.Is(err, polymer.ErrPending) {
		return pending(saved, retryInterval)
	}
	var failure *polymer.JobError
	if errors.As(err, &failure) {
		saved.LastFailure = failure
		return pending(saved, b.retry.delay(saved.Requests))
	}
	if err != nil {
		return settlement.Result{}, err
	}
	return pending(saved, 0)
}

func packMessage(proof []byte) ([]byte, error) {
	for name, method := range OracleABI.Methods {
		if method.Sig == "receiveMessage(bytes)" {
			return OracleABI.Pack(name, proof)
		}
	}
	return nil, errors.New("polymer receiveMessage ABI absent")
}

func (b *Backend) CheckAccess(ctx context.Context, evidence settlement.Evidence) error {
	_, fill, err := escrowprotocol.DecodeFulfillment(evidence, b.route, b.signer)
	if err != nil {
		return err
	}
	if b.proofs == nil {
		return errors.New("polymer proof access is not enabled")
	}
	job, err := b.proofs.RequestEVM(ctx, polymer.EVMLog{ChainID: b.route.DestinationChain, BlockNumber: fill.Log.BlockNumber, Index: fill.Log.Index})
	if err != nil {
		return err
	}
	for {
		_, err = b.proofs.Query(ctx, job)
		if !errors.Is(err, polymer.ErrPending) {
			return err
		}
		timer := time.NewTimer(retryInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
