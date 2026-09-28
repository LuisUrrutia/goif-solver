package evm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/redis/go-redis/v9"
)

type chainRPC struct {
	mu         sync.Mutex
	tx         *types.Transaction
	mined      bool
	broadcasts int
	header     *types.Header
}

func (c *chainRPC) ChainId() hexutil.Uint64 { return 84532 }
func (c *chainRPC) GetTransactionCount(common.Address, string) hexutil.Uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.mined {
		return 1
	}
	return 0
}
func (c *chainRPC) MaxPriorityFeePerGas() *hexutil.Big                { n := big.NewInt(1); return (*hexutil.Big)(n) }
func (c *chainRPC) GetBlockByNumber(string, bool) *types.Header       { return c.header }
func (c *chainRPC) BlockNumber() hexutil.Uint64                       { return 100 }
func (c *chainRPC) EstimateGas(map[string]interface{}) hexutil.Uint64 { return 50000 }
func (c *chainRPC) GetBalance(common.Address, string) *hexutil.Big {
	n := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	return (*hexutil.Big)(n)
}
func (c *chainRPC) SendRawTransaction(raw hexutil.Bytes) (common.Hash, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var tx types.Transaction
	if e := tx.UnmarshalBinary(raw); e != nil {
		return common.Hash{}, e
	}
	if c.tx != nil && c.tx.Hash() != tx.Hash() {
		return common.Hash{}, errors.New("different transaction rebroadcast")
	}
	c.tx = &tx
	c.broadcasts++
	return tx.Hash(), nil
}
func (c *chainRPC) GetTransactionReceipt(hash common.Hash) json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.mined || c.tx == nil || c.tx.Hash() != hash {
		return json.RawMessage("null")
	}
	receipt := &types.Receipt{Type: 2, Status: 1, CumulativeGasUsed: 50000, Logs: []*types.Log{}, TxHash: hash, GasUsed: 50000, EffectiveGasPrice: big.NewInt(3), BlockHash: c.header.Hash(), BlockNumber: c.header.Number}
	b, _ := json.Marshal(receipt)
	return b
}

func TestSenderRecoveryReusesSignedTransaction(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("run scripts/check.sh")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer client.Close()
	store, e := coordination.New(client, fmt.Sprintf("sender-%d", time.Now().UnixNano()))
	if e != nil {
		t.Fatal(e)
	}
	backend := &chainRPC{header: &types.Header{Number: big.NewInt(90), Difficulty: big.NewInt(0), BaseFee: big.NewInt(1), GasLimit: 30000000}}
	server := rpc.NewServer()
	if e = server.RegisterName("eth", backend); e != nil {
		t.Fatal(e)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	chain, e := Dial(t.Context(), httpServer.URL, 84532, 1000)
	if e != nil {
		t.Fatal(e)
	}
	defer chain.Close()
	key, e := crypto.GenerateKey()
	if e != nil {
		t.Fatal(e)
	}
	signer, e := NewLocalSigner(hexutil.Encode(crypto.FromECDSA(key)), crypto.PubkeyToAddress(key.PublicKey), []uint64{84532})
	if e != nil {
		t.Fatal(e)
	}
	sender := &Sender{Client: chain, Store: store, Signer: signer, Policy: SendPolicy{Chain: 84532, Confirmations: 2, MaxGas: 100000, MaxFee: big.NewInt(100)}}
	lease, e := store.Acquire(t.Context(), "order:test", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	to := common.HexToAddress("0x0000000000000000000000000000000000000123")
	if _, e = sender.Execute(t.Context(), lease, "test:fill", to, []byte{1, 2}); !errors.Is(e, ErrPending) {
		t.Fatal(e)
	}
	resource := SignerResource(84532, signer.Address())
	saved, e := store.Transaction(t.Context(), resource, "test:fill")
	if e != nil {
		t.Fatal(e)
	}
	// Simulate a new worker after broadcast but before the stage was persisted.
	if e = store.Release(t.Context(), lease); e != nil {
		t.Fatal(e)
	}
	lease, e = store.Acquire(t.Context(), "order:test", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = sender.Execute(t.Context(), lease, "test:fill", to, []byte{1, 2}); !errors.Is(e, ErrPending) {
		t.Fatal(e)
	}
	backend.mu.Lock()
	backend.mined = true
	backend.mu.Unlock()
	receipt, e := sender.Execute(t.Context(), lease, "test:fill", to, []byte{1, 2})
	if e != nil {
		t.Fatal(e)
	}
	if receipt.TxHash.Hex() != saved.Hash {
		t.Fatal("recovery changed transaction")
	}
	pending, e := store.Pending(context.Background(), resource)
	if e != nil || pending != "" {
		t.Fatalf("reservation remains %s %v", pending, e)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if backend.broadcasts != 2 {
		t.Fatalf("broadcasts=%d", backend.broadcasts)
	}
}
func TestLocalSignerRefusesUnverifiedChain(t *testing.T) {
	key, e := crypto.GenerateKey()
	if e != nil {
		t.Fatal(e)
	}
	if _, e = NewLocalSigner(hexutil.Encode(crypto.FromECDSA(key)), crypto.PubkeyToAddress(key.PublicKey), []uint64{1}); e == nil {
		t.Fatal("accepted mainnet signing")
	}
}
