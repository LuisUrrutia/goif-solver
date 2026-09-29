package escrow

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"testing/synctest"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/storage/memorystore"
	"github.com/LuisUrrutia/goif-solver/internal/transport"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

type rateLimitedLogs struct {
	*logChain
	limiter *transport.Limiter
}

func (c *rateLimitedLogs) BlockNumber(ctx context.Context) (uint64, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return 0, err
	}
	return c.logChain.BlockNumber(ctx)
}

func (c *rateLimitedLogs) HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	return c.logChain.HeaderByNumber(ctx, number)
}

func (c *rateLimitedLogs) FilterLogs(ctx context.Context, filter ethereum.FilterQuery) ([]types.Log, error) {
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	return c.logChain.FilterLogs(ctx, filter)
}

func TestDenseLogRangeMakesCheckpointProgress(t *testing.T) {
	for _, test := range []struct {
		name        string
		perBlock    int
		budget      time.Duration
		acceptDelay time.Duration
	}{
		{name: "RPC limited", perBlock: 4, budget: 45 * time.Second},
		{name: "single dense block", perBlock: 200, budget: 5 * time.Second, acceptDelay: 100 * time.Millisecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				log, chain, settler := openFixture(t)
				client := &rateLimitedLogs{logChain: &logChain{head: 151}, limiter: transport.NewLimiter(time.Second / 4)}
				for i := range 200 {
					entry := log
					entry.BlockNumber = 90 + uint64(i/test.perBlock)
					entry.Index = uint(i % test.perBlock)
					header := &types.Header{Number: new(big.Int).SetUint64(entry.BlockNumber), Difficulty: big.NewInt(0)}
					entry.BlockHash = header.Hash()
					entry.Topics = []common.Hash{log.Topics[0], common.BigToHash(big.NewInt(int64(i + 1)))}
					client.logs = append(client.logs, entry)
				}
				store := memorystore.New()
				accepted := make(map[string]int)
				emit := func(ctx context.Context, candidate intent.Candidate) error {
					if !waitForAcceptance(ctx, test.acceptDelay) {
						return ctx.Err()
					}
					_, err := store.Enqueue(ctx, candidate.Identity().Key(), string(candidate.Payload))
					if err == nil {
						accepted[candidate.ID]++
					}
					return err
				}

				var checkpoint logCheckpoint
				scans := 0
				for range 10 {
					scans++
					source := &LogSource{Client: client, Checkpoints: store, Settler: settler, ChainID: chain, Confirmations: 12, StartBlock: 80}
					ctx, cancel := context.WithTimeout(t.Context(), test.budget)
					err := source.Scan(ctx, emit)
					cancel()
					value, readErr := store.Checkpoint(t.Context(), string(source.Identity()))
					checkpoint = logCheckpoint{}
					if readErr != nil || value == "" || json.Unmarshal([]byte(value), &checkpoint) != nil {
						t.Fatalf("scan did not save progress: checkpoint=%q scan=%v read=%v", value, err, readErr)
					}
					if checkpoint.Block == 139 && checkpoint.LogIndex == nil {
						break
					}
				}

				stats, err := store.Stats(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				if stats.Outstanding != 200 || checkpoint.Block != 139 || checkpoint.LogIndex != nil {
					t.Fatalf("scan stalled: unique=%d/200 checkpoint=%+v", stats.Outstanding, checkpoint)
				}
				if test.acceptDelay == 0 && scans != 1 {
					t.Fatalf("repeated block reads exhausted the RPC budget: scans=%d", scans)
				}
				t.Logf("accepted %d intents in %d bounded scans", stats.Outstanding, scans)
				for id, count := range accepted {
					if count != 1 {
						t.Fatalf("replayed durably checkpointed intent %s %d times", id, count)
					}
				}
			})
		})
	}
}

func waitForAcceptance(ctx context.Context, delay time.Duration) bool {
	if delay == 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}

func TestPartialLogCheckpointReplaysUnacknowledgedTailAndStopsOnReorg(t *testing.T) {
	for _, reorg := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "reorg"}[reorg], func(t *testing.T) {
			log, chain, settler := openFixture(t)
			backend := &logChain{head: 102}
			for i := range 3 {
				entry := log
				entry.Index = uint(i)
				entry.Topics = []common.Hash{log.Topics[0], common.BigToHash(big.NewInt(int64(i + 1)))}
				backend.logs = append(backend.logs, entry)
			}
			checkpoint := &memoryCheckpoint{}
			source := LogSource{Client: backend, Checkpoints: checkpoint, Settler: settler, ChainID: chain, Confirmations: 12, StartBlock: 90}
			attempts := make(map[string]int)
			unavailable := errors.New("durable acceptance unavailable")
			emit := func(_ context.Context, candidate intent.Candidate) error {
				attempts[candidate.ID]++
				if candidate.ID == backend.logs[1].Topics[1].Hex() && attempts[candidate.ID] == 1 {
					return unavailable
				}
				return nil
			}

			if err := source.Scan(t.Context(), emit); !errors.Is(err, unavailable) {
				t.Fatal(err)
			}
			var saved logCheckpoint
			if err := json.Unmarshal([]byte(checkpoint.value), &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Block != 90 || saved.LogIndex == nil || *saved.LogIndex != 0 {
				t.Fatalf("checkpoint skipped an unacknowledged log: %+v", saved)
			}
			before := checkpoint.value
			backend.replaced = reorg
			restarted := source
			err := restarted.Scan(t.Context(), emit)

			if reorg {
				if err == nil || checkpoint.value != before || len(attempts) != 2 || attempts[backend.logs[1].Topics[1].Hex()] != 1 {
					t.Fatal("partial checkpoint survived a reorg", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for i, log := range backend.logs {
				want := 1
				if i == 1 {
					want = 2
				}
				if attempts[log.Topics[1].Hex()] != want {
					t.Fatalf("log %d delivered %d times, want %d", i, attempts[log.Topics[1].Hex()], want)
				}
			}
		})
	}
}

func TestLogCheckpointUpgradePreservesBacklogAndIsolatesPartialPositions(t *testing.T) {
	log, chain, settler := openFixture(t)
	backend := &logChain{head: 1000, logs: []types.Log{log}}
	store := memorystore.New()
	source := LogSource{Client: backend, Checkpoints: store, Settler: settler, ChainID: chain, Confirmations: 12, Lookback: 10}
	header, err := backend.HeaderByNumber(t.Context(), big.NewInt(89))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := json.Marshal(logCheckpoint{Block: 89, Hash: header.Hash()})
	if err != nil {
		t.Fatal(err)
	}
	oldKey := string(source.identity("open-v1"))
	if err := store.CommitCheckpoint(t.Context(), oldKey, "", string(legacy)); err != nil {
		t.Fatal(err)
	}
	deliveries := 0
	emit := func(context.Context, intent.Candidate) error { deliveries++; return nil }

	if err := source.Scan(t.Context(), emit); err != nil {
		t.Fatal(err)
	}
	if err := source.Scan(t.Context(), emit); err != nil {
		t.Fatal(err)
	}

	if deliveries != 1 || backend.queries[0].FromBlock.Uint64() != 90 {
		t.Fatalf("upgrade lost backlog outside lookback: deliveries=%d queries=%v", deliveries, backend.queries)
	}
	old, err := store.Checkpoint(t.Context(), oldKey)
	if err != nil || old != string(legacy) {
		t.Fatal("changed the older reader's checkpoint", err)
	}
	if string(source.Identity()) == oldKey {
		t.Fatal("partial and completed cursors share a key")
	}
}
