package escrow

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/storage/redisstore"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/redis/go-redis/v9"
)

type clusterLogChain struct{ logChain }

func (c *clusterLogChain) FilterLogs(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	all, err := c.logChain.FilterLogs(ctx, q)
	var filtered []types.Log
	for _, item := range all {
		for _, address := range q.Addresses {
			if item.Address == address {
				filtered = append(filtered, item)
			}
		}
	}
	return filtered, err
}

func TestClusterCheckpointsIdentifyTheSettler(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("run scripts/check.sh for real Redis tests")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer func() { _ = client.Close() }()
	store, err := redisstore.New(client, fmt.Sprintf("source-cluster-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	log, chain, settlerB := openFixture(t)
	backend := &clusterLogChain{logChain: logChain{head: 103, logs: []types.Log{log}}}
	sourceA := LogSource{Client: backend, Checkpoints: store, Settler: common.HexToAddress("0x1234"), ChainID: chain, Confirmations: 12, StartBlock: 80}
	sourceB := sourceA
	sourceB.Settler = settlerB
	deliveries := 0
	accept := func(context.Context, intent.Candidate) error { deliveries++; return nil }
	if err = sourceA.Scan(t.Context(), accept); err != nil {
		t.Fatal(err)
	}
	if err = sourceB.Scan(t.Context(), accept); err != nil {
		t.Fatal(err)
	}
	if deliveries != 1 {
		t.Fatal("another settler cursor skipped historical event")
	}
	if sourceA.Identity() == sourceB.Identity() {
		t.Fatal("different settlers share identity")
	}
	if err := sourceB.Scan(t.Context(), accept); err != nil || deliveries != 1 {
		t.Fatal("cursor was not reused for identical source", err)
	}
	sourceB.StartBlock--
	if err := sourceB.Scan(t.Context(), accept); err != nil || deliveries != 2 {
		t.Fatal("explicit backfill reused old cursor", err)
	}
}
