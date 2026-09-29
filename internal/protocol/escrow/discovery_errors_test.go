package escrow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/transport"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
)

type failingLogs struct {
	*logChain
	failure error
	failAt  int
	calls   int
}

func (c *failingLogs) next() error {
	c.calls++
	if c.calls == c.failAt {
		return c.failure
	}
	return nil
}

func (c *failingLogs) BlockNumber(ctx context.Context) (uint64, error) {
	if err := c.next(); err != nil {
		return 0, err
	}
	return c.logChain.BlockNumber(ctx)
}

func (c *failingLogs) HeaderByNumber(ctx context.Context, number *big.Int) (*types.Header, error) {
	if err := c.next(); err != nil {
		return nil, err
	}
	return c.logChain.HeaderByNumber(ctx, number)
}

func (c *failingLogs) FilterLogs(ctx context.Context, query ethereum.FilterQuery) ([]types.Log, error) {
	if err := c.next(); err != nil {
		return nil, err
	}
	return c.logChain.FilterLogs(ctx, query)
}

func TestDiscoveryClassifiesEveryRPCFailure(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, transport.ErrUnavailable} {
		for i, operation := range []string{"head", "checkpoint", "range", "logs", "log block", "final block"} {
			t.Run(operation+"/"+cause.Error(), func(t *testing.T) {
				log, chain, settler := openFixture(t)
				client := &failingLogs{logChain: &logChain{head: 100, logs: []types.Log{log}}, failAt: i + 1, failure: fmt.Errorf("https://rpc.invalid/synthetic-secret: %w", cause)}
				header, err := client.logChain.HeaderByNumber(t.Context(), big.NewInt(79))
				if err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(logCheckpoint{Block: 79, Hash: header.Hash()})
				if err != nil {
					t.Fatal(err)
				}
				source := LogSource{Client: client, Checkpoints: &memoryCheckpoint{value: string(raw)}, Settler: settler, ChainID: chain}

				err = source.Scan(t.Context(), func(context.Context, intent.Candidate) error { return nil })

				if !errors.Is(err, cause) || strings.Contains(err.Error(), "synthetic-secret") {
					t.Fatalf("RPC error lost classification or leaked details: %v", err)
				}
				if client.calls != client.failAt {
					t.Fatalf("continued after RPC failure: %d calls, expected %d", client.calls, client.failAt)
				}
			})
		}
	}
}
