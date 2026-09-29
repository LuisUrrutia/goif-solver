package evm

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/storage/memorystore"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
)

type cleanupJournal struct {
	*memorystore.Store
	cancel      context.CancelFunc
	releaseErr  error
	deadline    time.Time
	interrupted bool
	stall       bool
}

func (j *cleanupJournal) Pending(ctx context.Context, _ string) (string, error) {
	j.cancel()
	return "", ctx.Err()
}

func (j *cleanupJournal) Release(ctx context.Context, lease coordination.Lease) error {
	j.deadline, _ = ctx.Deadline()
	j.interrupted = ctx.Err() != nil
	if j.stall {
		<-ctx.Done()
	}
	j.releaseErr = j.Store.Release(ctx, lease)
	return j.releaseErr
}

func TestSenderCleanupSurvivesCancellationAndHasDeadline(t *testing.T) {
	for _, operation := range []string{"execute", "recover"} {
		for _, stall := range []bool{false, true} {
			name := operation + "/responsive"
			if stall {
				name = operation + "/stalled"
			}
			t.Run(name, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithCancel(t.Context())
					defer cancel()
					journal := &cleanupJournal{Store: memorystore.New(), cancel: cancel, stall: stall}
					key, err := crypto.GenerateKey()
					if err != nil {
						t.Fatal(err)
					}
					signer, err := NewLocalSigner(hexutil.Encode(crypto.FromECDSA(key)), crypto.PubkeyToAddress(key.PublicKey), []uint64{1337})
					if err != nil {
						t.Fatal(err)
					}
					sender := &Sender{Store: journal, Signer: signer, Policy: SendPolicy{Chain: 1337, Enabled: true}}
					start := time.Now()

					if operation == "execute" {
						_, err = sender.Execute(ctx, coordination.Lease{}, SendRequest{Operation: "test:fill"})
					} else {
						err = sender.Recover(ctx)
					}

					if !errors.Is(err, context.Canceled) {
						t.Fatalf("operation lost cancellation: %v", err)
					}
					if journal.interrupted || journal.deadline.Sub(start) != 5*time.Second {
						t.Fatalf("cleanup context: interrupted=%t deadline=%s", journal.interrupted, journal.deadline)
					}
					if stall {
						if !errors.Is(journal.releaseErr, context.DeadlineExceeded) || time.Since(start) != 5*time.Second {
							t.Fatalf("cleanup did not respect its deadline: %v", journal.releaseErr)
						}
					} else {
						if journal.releaseErr != nil {
							t.Fatal(journal.releaseErr)
						}
						if _, err := journal.Acquire(t.Context(), SignerResource(1337, signer.Address()), time.Minute); err != nil {
							t.Fatal("cleanup did not release the signer", err)
						}
					}
				})
			})
		}
	}
}
