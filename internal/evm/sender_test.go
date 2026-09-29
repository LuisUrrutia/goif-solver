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

	"github.com/LuisUrrutia/goif-solver/internal/storage/redisstore"

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
	reverted   bool
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
	if c.reverted {
		receipt.Status = types.ReceiptStatusFailed
	}
	b, _ := json.Marshal(receipt)
	return b
}

func TestSenderRecoveryReusesSignedTransaction(t *testing.T) {
	store := senderRedisStore(t)
	sender, backend := senderFixture(t, store)
	lease, e := store.Acquire(t.Context(), "order:test", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	to := common.HexToAddress("0x0000000000000000000000000000000000000123")
	if _, e = sender.Execute(t.Context(), lease, SendRequest{Operation: "test:fill", To: to, Data: []byte{1, 2}}); !errors.Is(e, ErrPending) {
		t.Fatal(e)
	}
	resource := SignerResource(84532, sender.Signer.Address())
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
	if _, e = sender.Execute(t.Context(), lease, SendRequest{Operation: "test:fill", To: to, Data: []byte{1, 2}}); !errors.Is(e, ErrPending) {
		t.Fatal(e)
	}
	backend.mu.Lock()
	backend.mined = true
	backend.mu.Unlock()
	receipt, e := sender.Execute(t.Context(), lease, SendRequest{Operation: "test:fill", To: to, Data: []byte{1, 2}})
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

func senderRedisStore(t *testing.T) coordination.Backend {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("run scripts/check.sh")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	store, e := redisstore.New(client, fmt.Sprintf("sender-%d", time.Now().UnixNano()))
	if e != nil {
		t.Fatal(e)
	}
	return store
}

func senderFixture(t *testing.T, store coordination.Backend) (*Sender, *chainRPC) {
	t.Helper()
	backend := &chainRPC{header: &types.Header{Number: big.NewInt(90), Difficulty: big.NewInt(0), BaseFee: big.NewInt(1), GasLimit: 30000000}}
	server := rpc.NewServer()
	if e := server.RegisterName("eth", backend); e != nil {
		t.Fatal(e)
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	chain, e := NewClient(t.Context(), []RPCSettings{{URL: httpServer.URL}}, 84532, 1000)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(chain.Close)
	key, e := crypto.GenerateKey()
	if e != nil {
		t.Fatal(e)
	}
	signer, e := NewLocalSigner(hexutil.Encode(crypto.FromECDSA(key)), crypto.PubkeyToAddress(key.PublicKey), []uint64{84532})
	if e != nil {
		t.Fatal(e)
	}
	sender := &Sender{Client: chain, Store: store, Signer: signer, Policy: SendPolicy{Enabled: true, Chain: 84532, Confirmations: 2, MaxGas: 100000, MaxFee: big.NewInt(100)}}
	return sender, backend
}

func TestLocalSignerUsesConfiguredChains(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signer, err := NewLocalSigner(hexutil.Encode(crypto.FromECDSA(key)), crypto.PubkeyToAddress(key.PublicKey), []uint64{1337})
	if err != nil {
		t.Fatal(err)
	}
	tx := types.NewTx(&types.DynamicFeeTx{ChainID: big.NewInt(1337), Gas: 21000, GasFeeCap: big.NewInt(1), GasTipCap: big.NewInt(1)})
	if _, err = signer.SignTx(t.Context(), tx, 1337); err != nil {
		t.Fatal(err)
	}
	if _, err = signer.SignTx(t.Context(), tx, 1); err == nil {
		t.Fatal("accepted chain outside configured signer policy")
	}
	sender := Sender{Policy: SendPolicy{Chain: 1337}}
	if _, err = sender.Execute(t.Context(), coordination.Lease{}, SendRequest{}); err == nil {
		t.Fatal("signing must be explicitly enabled")
	}
}
