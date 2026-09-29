package polymerevm

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"github.com/LuisUrrutia/goif-solver/internal/settlement/polymer"
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
	stateVersion  = 1
	retryInterval = 2 * time.Second
)

type checkpoint struct {
	Proof   []byte `json:"proof,omitempty"`
	Job     uint64 `json:"job,omitempty"`
	Version uint8  `json:"version"`
}

type Backend struct {
	clients map[uint64]*ethclient.Client
	sender  *evm.Sender
	proofs  *polymer.Client
	id      settlement.ID
	route   evm.Route
	signer  common.Address
}

var (
	_ settlement.Backend       = (*Backend)(nil)
	_ settlement.AccessChecker = (*Backend)(nil)
)

func NewBackend(id settlement.ID, route evm.Route, signer common.Address, clients map[uint64]*ethclient.Client, sender *evm.Sender, proofs *polymer.Client) (*Backend, error) {
	if id == "" || route.Settlement != id || clients[route.OriginChain] == nil || clients[route.DestinationChain] == nil || signer == (common.Address{}) {
		return nil, errors.New("incomplete Polymer route binding")
	}
	return &Backend{id: id, route: route, signer: signer, clients: clients, sender: sender, proofs: proofs}, nil
}

func (b *Backend) Verify(ctx context.Context) error {
	for _, side := range []struct {
		chain  uint64
		oracle common.Address
	}{{b.route.OriginChain, b.route.InputOracle}, {b.route.DestinationChain, b.route.OutputOracle}} {
		code, err := b.clients[side.chain].CodeAt(ctx, side.oracle, nil)
		if err != nil {
			return errors.New("oracle runtime query failed")
		}
		if crypto.Keccak256Hash(code) != oracleRuntime {
			return errors.New("oracle runtime is incompatible with Polymer")
		}
	}
	return nil
}

func (b *Backend) Inspect(ctx context.Context, evidence settlement.Evidence) (settlement.Verification, error) {
	v, fill, err := evm.DecodeFulfillment(evidence, b.route, b.signer)
	if err != nil {
		return settlement.Verification{}, err
	}
	return b.inspect(ctx, v, fill)
}

func (b *Backend) inspect(ctx context.Context, v evm.Validated, fill evm.FillEvent) (settlement.Verification, error) {
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
	v, fill, err := evm.DecodeFulfillment(request.Evidence, b.route, b.signer)
	if err != nil {
		return settlement.Result{}, err
	}
	if v.ID.Hex() != request.IntentID || request.Lease.Resource != coordination.IntentResource(request.IntentID) {
		return settlement.Result{}, errors.New("settlement lease does not match fulfillment")
	}
	saved := checkpoint{Version: stateVersion}
	if len(state) > 0 {
		if json.Unmarshal(state, &saved) != nil || saved.Version != stateVersion || saved.Job == 0 {
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
		if _, err = b.sender.Execute(ctx, request.Lease, request.IntentID+":settlement:"+string(b.id)+":relay", b.route.InputOracle, data); err != nil {
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
	if saved.Job == 0 {
		saved.Job, err = b.proofs.RequestEVM(ctx, polymer.EVMLog{ChainID: b.route.DestinationChain, BlockNumber: fill.Log.BlockNumber, Index: fill.Log.Index})
		if err != nil {
			return settlement.Result{}, err
		}
		return pending(saved, 0)
	}
	saved.Proof, err = b.proofs.Query(ctx, saved.Job)
	if errors.Is(err, polymer.ErrPending) {
		return pending(saved, retryInterval)
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
	_, fill, err := evm.DecodeFulfillment(evidence, b.route, b.signer)
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
