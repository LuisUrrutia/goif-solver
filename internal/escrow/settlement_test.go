package escrow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"github.com/LuisUrrutia/goif-solver/internal/storage/memorystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// An event-driven test adapter has neither a remote job nor a proof query API.
type eventAttestation struct {
	delivered <-chan struct{}
	premature bool
}

func (a eventAttestation) Verify(context.Context) error { return nil }
func (a eventAttestation) Inspect(ctx context.Context, _ settlement.Evidence) (settlement.Verification, error) {
	select {
	case <-ctx.Done():
		return settlement.Verification{}, ctx.Err()
	case <-a.delivered:
		return settlement.Verification{Verified: true, Reference: "attestation"}, nil
	default:
		return settlement.Verification{}, nil
	}
}

func (a eventAttestation) Advance(ctx context.Context, r settlement.Request, _ json.RawMessage) (settlement.Result, error) {
	verification, err := a.Inspect(ctx, r.Evidence)
	if err != nil {
		return settlement.Result{}, err
	}
	if verification.Verified || a.premature {
		return settlement.Result{Status: settlement.Verified}, nil
	}
	return settlement.Result{Status: settlement.Pending, RetryAfter: time.Second}, nil
}

func filledExecution(t *testing.T, backend settlement.Backend) *execution {
	t.Helper()
	c, err := loadTestPolicy("../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../lifi/testdata/pilot-order.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope lifi.Envelope
	if err = json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	order, err := escrowprotocol.Parse(envelope.Order)
	if err != nil {
		t.Fatal(err)
	}
	v := escrowprotocol.Validated{ID: common.HexToHash(envelope.Meta.ID), Order: order, Route: c.Routes[0]}
	event := escrowprotocol.OutputABI.Events["OutputFilled"]
	solver := evm.AddressWord(c.Signers[0].Address)
	const timestamp = uint32(1790619040)
	data, err := event.Inputs.NonIndexed().Pack(solver, timestamp, order.Outputs[0], order.Outputs[0].Amount)
	if err != nil {
		t.Fatal(err)
	}
	fill := escrowprotocol.FillEvent{Solver: solver, Timestamp: timestamp, Log: types.Log{
		Address: v.Route.OutputSettler, Topics: []common.Hash{event.ID, v.ID}, Data: data,
		BlockNumber: 90, BlockHash: common.HexToHash("0x02"), Index: 7,
	}}
	work := Work{Route: v.Route.Name, Settlement: v.Route.Settlement, Version: c.Version, Envelope: envelope.Intent()}
	payload, err := json.Marshal(work)
	if err != nil {
		t.Fatal(err)
	}
	store := memorystore.New()
	if _, err = store.Enqueue(t.Context(), (intent.Identity{Kind: escrowprotocol.IntentKind, NativeID: v.ID.Hex()}).Key(), string(payload)); err != nil {
		t.Fatal(err)
	}
	lease, err := store.Acquire(t.Context(), coordination.IntentResource((intent.Identity{Kind: escrowprotocol.IntentKind, NativeID: v.ID.Hex()}).Key()), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Advance(t.Context(), lease, (intent.Identity{Kind: escrowprotocol.IntentKind, NativeID: v.ID.Hex()}).Key(), intent.Discovered, Filled, "", false, 0); err != nil {
		t.Fatal(err)
	}
	record, err := store.Record(t.Context(), (intent.Identity{Kind: escrowprotocol.IntentKind, NativeID: v.ID.Hex()}).Key())
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{Config: c, Store: store, Settlements: map[string]settlement.Backend{work.Route: backend}}
	return &execution{ctx: t.Context(), e: engine, lease: lease, record: record, work: &work, v: v, progress: &Progress{Fill: &fill}, durable: &intent.Progress{}}
}

func TestEventSettlementCanResumeWithoutProofJobs(t *testing.T) {
	delivered := make(chan struct{})
	x := filledExecution(t, eventAttestation{delivered: delivered})
	if err := x.onFilled(); err != nil {
		t.Fatal(err)
	}
	record, err := x.e.Store.Record(t.Context(), x.record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Stage != Filled {
		t.Fatal("advanced before attestation")
	}
	var durable intent.Progress
	if err = json.Unmarshal([]byte(record.Detail), &durable); err != nil {
		t.Fatal(err)
	}
	var progress Progress
	if err = json.Unmarshal(durable.State, &progress); err != nil {
		t.Fatal(err)
	}
	close(delivered)
	resumed := *x
	resumed.record = record
	resumed.progress = &progress
	resumed.durable = &durable
	resumed.e = &Engine{Store: x.e.Store, Settlements: map[string]settlement.Backend{x.work.Route: eventAttestation{delivered: delivered}}}
	if err = resumed.onFilled(); err != nil {
		t.Fatal(err)
	}
	record, err = x.e.Store.Record(t.Context(), x.record.ID)
	if err != nil || record.Stage != Proven {
		t.Fatal("attestation did not unblock settlement", record.Stage, err)
	}
}

func TestSettlementMustBeVerifiedBeforeClaim(t *testing.T) {
	x := filledExecution(t, eventAttestation{delivered: make(chan struct{}), premature: true})
	if err := x.onFilled(); err == nil {
		t.Fatal("backend completion bypassed verification")
	}
	record, err := x.e.Store.Record(t.Context(), x.record.ID)
	if err != nil || record.Stage != Filled {
		t.Fatal("unverified settlement advanced", err)
	}
}

func TestPersistedSettlementBindingCannotChange(t *testing.T) {
	x := filledExecution(t, eventAttestation{})
	x.e.Config.Routes[0].Settlement = "different-backend"
	err := x.e.Step(t.Context(), x.lease, x.record)
	if err == nil || errors.Is(err, intent.ErrObserve) {
		t.Fatal("persisted binding accepted a replacement backend", err)
	}
}
