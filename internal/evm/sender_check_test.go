package evm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/storage/memorystore"
	"github.com/ethereum/go-ethereum/common"
)

func TestSenderChecksNewBytesUnderSignerLease(t *testing.T) {
	for name, newStore := range map[string]func(*testing.T) coordination.Backend{
		"memory": func(*testing.T) coordination.Backend { return memorystore.New() },
		"redis":  senderRedisStore,
	} {
		t.Run(name, func(t *testing.T) {
			store := newStore(t)
			sender, backend := senderFixture(t, store)
			lease, err := store.Acquire(t.Context(), coordination.IntentResource("guarded"), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			resource := SignerResource(sender.Policy.Chain, sender.Signer.Address())
			denied := errors.New("prerequisite unavailable")
			checkErr := denied
			checks := 0
			request := SendRequest{Operation: "guarded:fill", To: common.HexToAddress("0x1234"), Data: []byte{1, 2}, Check: func(ctx context.Context) error {
				checks++
				if _, err := store.Acquire(ctx, resource, time.Minute); !errors.Is(err, coordination.ErrBusy) {
					t.Fatalf("check ran without signer ownership: %v", err)
				}
				return checkErr
			}}

			if _, err := sender.Execute(t.Context(), lease, request); !errors.Is(err, denied) {
				t.Fatal(err)
			}
			if _, err := store.Transaction(t.Context(), resource, request.Operation); !errors.Is(err, coordination.ErrNotFound) {
				t.Fatalf("failed check prepared bytes: %v", err)
			}
			checkErr = nil
			if _, err := sender.Execute(t.Context(), lease, request); !errors.Is(err, ErrPending) {
				t.Fatal(err)
			}
			checkErr = denied
			if _, err := sender.Execute(t.Context(), lease, request); !errors.Is(err, ErrPending) {
				t.Fatalf("recovery repeated check: %v", err)
			}
			other := request
			other.Operation = "guarded:next"
			if _, err := sender.Execute(t.Context(), lease, other); !errors.Is(err, ErrPending) {
				t.Fatal(err)
			}
			if checks != 2 {
				t.Fatal("checked before the preceding transaction reached finality", checks)
			}
			backend.mu.Lock()
			backend.mined = true
			backend.mu.Unlock()
			if _, err := sender.Execute(t.Context(), lease, other); !errors.Is(err, denied) {
				t.Fatal(err)
			}
			if _, err := sender.Execute(t.Context(), lease, request); err != nil {
				t.Fatal(err)
			}

			if checks != 3 {
				t.Fatal("completed operation repeated check", checks)
			}
		})
	}
}
