package evm

import (
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/storage/memorystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestSenderFinalizedOperationIgnoresUnrelatedReservation(t *testing.T) {
	stores := map[string]func(*testing.T) coordination.Backend{
		"memory": func(*testing.T) coordination.Backend { return memorystore.New() },
		"redis":  senderRedisStore,
	}
	for name, newStore := range stores {
		t.Run(name, func(t *testing.T) {
			for _, scenario := range []struct {
				name      string
				err       string
				held      bool
				missing   bool
				reorg     bool
				unfinal   bool
				different bool
				reverted  bool
			}{
				{name: "pending"},
				{name: "leased", held: true},
				{name: "missing receipt", missing: true, err: ErrPending.Error()},
				{name: "changed block", reorg: true, err: "finalized receipt differs from journal"},
				{name: "insufficient finality", unfinal: true, err: ErrPending.Error()},
				{name: "different request", different: true, err: "operation differs from prepared transaction"},
				{name: "reverted", reverted: true, err: ErrReverted.Error()},
			} {
				t.Run(scenario.name, func(t *testing.T) {
					store := newStore(t)
					sender, backend := senderFixture(t, store)
					leaseA, err := store.Acquire(t.Context(), coordination.IntentResource("a"), time.Minute)
					if err != nil {
						t.Fatal(err)
					}
					to := common.HexToAddress("0x0000000000000000000000000000000000000123")
					if _, err = sender.Execute(t.Context(), leaseA, SendRequest{Operation: "a:claim", To: to, Data: []byte{1, 2}}); !errors.Is(err, ErrPending) {
						t.Fatal(err)
					}
					backend.mu.Lock()
					backend.mined = true
					backend.reverted = scenario.reverted
					backend.mu.Unlock()
					if err = sender.Recover(t.Context()); err != nil && !(scenario.reverted && errors.Is(err, ErrReverted)) {
						t.Fatal(err)
					}
					resource := SignerResource(sender.Policy.Chain, sender.Signer.Address())
					before, err := store.TransactionOutcome(t.Context(), resource, "a:claim")
					if err != nil || before.State != coordination.Finalized {
						t.Fatalf("missing finalized outcome: %+v %v", before, err)
					}
					signerLease := reserveNextTransaction(t, store, sender, to)
					if !scenario.held {
						if err = store.Release(t.Context(), signerLease); err != nil {
							t.Fatal(err)
						}
					}
					backend.mu.Lock()
					backend.mined = !scenario.missing
					if scenario.reorg {
						backend.header.Extra = []byte{1}
					}
					expected := backend.tx.Hash()
					backend.mu.Unlock()
					if scenario.unfinal {
						sender.Policy.Confirmations = 20
					}
					data := []byte{1, 2}
					if scenario.different {
						data = []byte{9}
					}

					receipt, err := sender.Execute(t.Context(), leaseA, SendRequest{Operation: "a:claim", To: to, Data: data})

					if scenario.err == "" {
						if err != nil || receipt == nil || receipt.TxHash != expected {
							t.Fatalf("finalized operation blocked: receipt=%v err=%v", receipt, err)
						}
					} else if err == nil || !strings.Contains(err.Error(), scenario.err) {
						t.Fatalf("expected %q, got %v", scenario.err, err)
					}
					pending, err := store.Pending(t.Context(), resource)
					if err != nil || pending != "b:fill" {
						t.Fatalf("changed unrelated reservation: %s %v", pending, err)
					}
					after, err := store.TransactionOutcome(t.Context(), resource, "a:claim")
					if err != nil || after != before {
						t.Fatalf("changed immutable outcome: %+v %v", after, err)
					}
					if scenario.held {
						if err = store.Renew(t.Context(), signerLease, time.Minute); err != nil {
							t.Fatalf("changed unrelated lease: %v", err)
						}
					}
					backend.mu.Lock()
					defer backend.mu.Unlock()
					if backend.broadcasts != 1 {
						t.Fatalf("replayed finalized transaction: broadcasts=%d", backend.broadcasts)
					}
				})
			}
		})
	}
}

func reserveNextTransaction(t *testing.T, store coordination.Backend, sender *Sender, to common.Address) coordination.Lease {
	t.Helper()
	intentLease, err := store.Acquire(t.Context(), coordination.IntentResource("b"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	signerLease, err := store.Acquire(t.Context(), SignerResource(sender.Policy.Chain, sender.Signer.Address()), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := sender.Signer.SignTx(t.Context(), types.NewTx(&types.DynamicFeeTx{ChainID: new(big.Int).SetUint64(sender.Policy.Chain), Nonce: 1, GasTipCap: big.NewInt(1), GasFeeCap: big.NewInt(3), Gas: 60000, To: &to, Value: new(big.Int), Data: []byte{3, 4}}), sender.Policy.Chain)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := tx.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(transactionMetadata{Nonce: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Prepare(t.Context(), intentLease, signerLease, coordination.Transaction{Operation: "b:fill", Raw: hexutil.Encode(raw), Hash: tx.Hash().Hex(), Codec: transactionCodec, Metadata: string(metadata)}); err != nil {
		t.Fatal(err)
	}
	return signerLease
}
