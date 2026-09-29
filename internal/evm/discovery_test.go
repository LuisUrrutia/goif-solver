package evm

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"testing"

	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

type memoryCheckpoint struct{ value string }

func (m *memoryCheckpoint) Checkpoint(context.Context, string) (string, error) { return m.value, nil }

func (m *memoryCheckpoint) CommitCheckpoint(_ context.Context, _ string, before, after string) error {
	if m.value != before {
		return errors.New("concurrent update")
	}
	m.value = after
	return nil
}

type logChain struct {
	head     uint64
	logs     []types.Log
	replaced bool
	queries  []ethereum.FilterQuery
}

func (c *logChain) BlockNumber(context.Context) (uint64, error) { return c.head, nil }
func (c *logChain) HeaderByNumber(_ context.Context, n *big.Int) (*types.Header, error) {
	header := &types.Header{Number: n, Difficulty: big.NewInt(0)}
	if c.replaced {
		header.Extra = []byte("replacement")
	}
	return header, nil
}

func (c *logChain) FilterLogs(_ context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	c.queries = append(c.queries, q)
	out := []types.Log{}
	for _, log := range c.logs {
		if log.BlockNumber >= q.FromBlock.Uint64() && log.BlockNumber <= q.ToBlock.Uint64() {
			out = append(out, log)
		}
	}
	return out, nil
}

func openFixture(t testing.TB) (types.Log, uint64, common.Address) {
	t.Helper()
	data, err := os.ReadFile("../lifi/testdata/pilot-order.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Order   OrderData `json:"order"`
		Settler string    `json:"inputSettler"`
		Meta    struct {
			ID string `json:"onChainOrderId"`
		} `json:"meta"`
	}
	if err = json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	order, err := Parse(envelope.Order)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := openEvent().Inputs.NonIndexed().Pack(order)
	if err != nil {
		t.Fatal(err)
	}
	header := &types.Header{Number: big.NewInt(90), Difficulty: big.NewInt(0)}
	return types.Log{Address: common.HexToAddress(envelope.Settler), Topics: []common.Hash{openEvent().ID, common.HexToHash(envelope.Meta.ID)}, Data: encoded, BlockNumber: 90, BlockHash: header.Hash()}, order.OriginChainId.Uint64(), common.HexToAddress(envelope.Settler)
}

func TestLogsReplayOnlyAfterDurableAcceptanceAndStopOnReorg(t *testing.T) {
	log, chain, settler := openFixture(t)
	backend := &logChain{head: 100, logs: []types.Log{log}}
	checkpoint := &memoryCheckpoint{}
	source := LogSource{Client: backend, Checkpoints: checkpoint, Name: "source", Settler: settler, ChainID: chain, Confirmations: 12, StartBlock: 80}
	deliveries := 0
	accept := func(context.Context, intent.Candidate) error { deliveries++; return nil }
	if err := source.Scan(t.Context(), accept); err != nil {
		t.Fatal(err)
	}
	if deliveries != 0 {
		t.Fatal("unconfirmed log delivered")
	}
	backend.head = 103
	failed := errors.New("Redis unavailable")
	before := checkpoint.value
	if err := source.Scan(t.Context(), func(context.Context, intent.Candidate) error { return failed }); !errors.Is(err, failed) {
		t.Fatal(err)
	}
	if checkpoint.value != before {
		t.Fatal("advanced without durable acknowledgment")
	}
	if err := source.Scan(t.Context(), accept); err != nil {
		t.Fatal(err)
	}
	if err := source.Scan(t.Context(), accept); err != nil {
		t.Fatal(err)
	}
	if deliveries != 1 {
		t.Fatalf("delivered %d", deliveries)
	}
	backend.replaced = true
	if err := source.Scan(t.Context(), accept); err == nil {
		t.Fatal("deep reorg accepted")
	}
	if deliveries != 1 {
		t.Fatal("delivered through reorg")
	}
}

func TestDecodeOpenUsesFullABIAndRejectsRemovedOrOtherChain(t *testing.T) {
	log, chain, settler := openFixture(t)
	candidate, err := DecodeOpen(log, chain, settler)
	if err != nil || candidate.ID != log.Topics[1].Hex() {
		t.Fatalf("decode: %v", err)
	}
	var data IntentData
	if json.Unmarshal(candidate.Payload, &data) != nil {
		t.Fatal("invalid payload")
	}
	order, err := Parse(data.Order)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := openEvent().Inputs.NonIndexed().Pack(order)
	if err != nil || string(encoded) != string(log.Data) {
		t.Fatal("ABI round trip changed")
	}
	if _, err = DecodeOpen(log, chain+1, settler); !errors.Is(err, intent.ErrRejected) {
		t.Fatal("wrong chain accepted")
	}
	log.Removed = true
	if _, err = DecodeOpen(log, chain, settler); !errors.Is(err, intent.ErrRejected) {
		t.Fatal("removed log accepted")
	}
}

func BenchmarkDecodeOpen(b *testing.B) {
	log, chain, settler := openFixture(b)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := DecodeOpen(log, chain, settler); err != nil {
			b.Fatal(err)
		}
	}
}
