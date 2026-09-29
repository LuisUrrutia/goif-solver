package escrow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/storage/redisstore"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"github.com/LuisUrrutia/goif-solver/internal/settlement/polymer"
	polymerevm "github.com/LuisUrrutia/goif-solver/internal/settlement/polymer/evm"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/redis/go-redis/v9"
)

type testRouteVerifier struct{}

func (testRouteVerifier) Verify(context.Context, evm.Route) error { return nil }

type routeChain struct {
	mu                                sync.Mutex
	chain                             uint64
	header                            *types.Header
	order                             evm.Validated
	signer                            common.Address
	nonce                             uint64
	approved, filled, proven, settled bool
	fills, claims                     int
	receipts                          map[common.Hash]*types.Receipt
}

func (c *routeChain) ChainId() hexutil.Uint64                     { return hexutil.Uint64(c.chain) }
func (c *routeChain) GetBlockByNumber(string, bool) *types.Header { return c.header }
func (c *routeChain) BlockNumber() hexutil.Uint64                 { return 100 }
func (c *routeChain) GetTransactionCount(common.Address, string) hexutil.Uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return hexutil.Uint64(c.nonce)
}

func (c *routeChain) MaxPriorityFeePerGas() *hexutil.Big {
	n := big.NewInt(1)
	return (*hexutil.Big)(n)
}

func (c *routeChain) GetBalance(common.Address, string) *hexutil.Big {
	n := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	return (*hexutil.Big)(n)
}
func (c *routeChain) EstimateGas(map[string]interface{}) hexutil.Uint64 { return 100000 }
func (c *routeChain) Call(call map[string]json.RawMessage, block string) (hexutil.Bytes, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var data hexutil.Bytes
	if b, ok := call["input"]; ok {
		if err := json.Unmarshal(b, &data); err != nil {
			return nil, err
		}
	} else {
		if err := json.Unmarshal(call["data"], &data); err != nil {
			return nil, err
		}
	}
	for _, contract := range []abi.ABI{evm.InputABI, evm.OutputABI, polymerevm.OracleABI, evm.TokenABI} {
		method, err := contract.MethodById(data)
		if err != nil {
			continue
		}
		var value interface{}
		switch method.RawName {
		case "orderIdentifier":
			value = [32]byte(c.order.ID)
		case "orderStatus":
			value = uint8(1)
			if c.settled {
				value = uint8(2)
			}
		case "governanceFee", "nextGovernanceFee":
			value = uint64(0)
		case "balanceOf":
			value = big.NewInt(100000000)
		case "allowance":
			value = big.NewInt(0)
			if c.approved {
				value = big.NewInt(100000000)
			}
		case "getFillRecord":
			value = [32]byte{}
			if c.filled {
				value = [32]byte{1}
			}
		case "isProven":
			value = c.proven
		default:
			return nil, fmt.Errorf("unexpected call %s", method.RawName)
		}
		return method.Outputs.Pack(value)
	}
	return nil, errors.New("unknown call selector")
}

func (c *routeChain) GetTransactionReceipt(hash common.Hash) json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	r := c.receipts[hash]
	if r == nil {
		return json.RawMessage("null")
	}
	b, _ := json.Marshal(r)
	return b
}

func (c *routeChain) SendRawTransaction(raw hexutil.Bytes) (common.Hash, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var tx types.Transaction
	if err := tx.UnmarshalBinary(raw); err != nil {
		return common.Hash{}, err
	}
	if _, ok := c.receipts[tx.Hash()]; ok {
		return tx.Hash(), nil
	}
	if tx.Nonce() != c.nonce {
		return common.Hash{}, errors.New("incorrect nonce")
	}
	c.nonce++
	receipt := &types.Receipt{Type: 2, Status: 1, CumulativeGasUsed: 100000, Logs: []*types.Log{}, TxHash: tx.Hash(), GasUsed: 100000, EffectiveGasPrice: big.NewInt(3), BlockHash: c.header.Hash(), BlockNumber: c.header.Number}
	for _, contract := range []abi.ABI{evm.TokenABI, evm.OutputABI, polymerevm.OracleABI, evm.InputABI} {
		method, err := contract.MethodById(tx.Data())
		if err != nil {
			continue
		}
		switch method.RawName {
		case "approve":
			c.approved = true
		case "fillOrderOutputs":
			if c.filled {
				return common.Hash{}, errors.New("duplicate fill")
			}
			c.filled = true
			c.fills++
			event := evm.OutputABI.Events["OutputFilled"]
			data, err := event.Inputs.NonIndexed().Pack(evm.AddressWord(c.signer), uint32(time.Now().Unix()), c.order.Order.Outputs[0], c.order.Order.Outputs[0].Amount)
			if err != nil {
				return common.Hash{}, err
			}
			receipt.Logs = []*types.Log{{Address: c.order.Route.OutputSettler, Topics: []common.Hash{event.ID, c.order.ID}, Data: data, BlockNumber: 90, BlockHash: c.header.Hash(), TxHash: tx.Hash(), Index: 7}}
		case "receiveMessage":
			c.proven = true
		case "finalise":
			if !c.proven {
				return common.Hash{}, errors.New("claim before proof")
			}
			c.settled = true
			c.claims++
		default:
			return common.Hash{}, fmt.Errorf("unexpected transaction %s", method.RawName)
		}
		c.receipts[tx.Hash()] = receipt
		return tx.Hash(), nil
	}
	return common.Hash{}, errors.New("unknown transaction selector")
}

func TestSepoliaPolymerLifecycleAcrossWorkerRestarts(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("run scripts/check.sh")
	}
	redisClient := redis.NewClient(&redis.Options{Addr: addr})
	defer func() { _ = redisClient.Close() }()
	store, err := redisstore.New(redisClient, fmt.Sprintf("engine-%d", time.Now().UnixNano()))
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Load("../../config/sepolia.json")
	if err != nil {
		t.Fatal(err)
	}
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	address := crypto.PubkeyToAddress(key.PublicKey)
	c.Signers[0].Address = address
	signer, err := evm.NewLocalSigner(hexutil.Encode(crypto.FromECDSA(key)), address, []uint64{11155111, 84532})
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
	envelope.Order.FillDeadline = strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	envelope.Order.Expires = strconv.FormatInt(time.Now().Add(24*time.Hour).Unix(), 10)
	envelope.Order.Outputs[0].Context = "0x"
	validated, err := evm.Validate(envelope.Intent(), c.Routes[0], address, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	work, _ := json.Marshal(Work{Settlement: c.Routes[0].Settlement, Version: c.Version, Route: c.Routes[0].Name, Envelope: envelope.Intent()})
	if _, err = store.Enqueue(t.Context(), validated.ID.Hex(), string(work)); err != nil {
		t.Fatal(err)
	}
	clients := map[uint64]*ethclient.Client{}
	backends := map[uint64]*routeChain{}
	senders := map[uint64]*evm.Sender{}
	for _, chain := range c.Chains {
		backend := &routeChain{chain: chain.ID, header: &types.Header{Number: big.NewInt(90), Difficulty: big.NewInt(0), BaseFee: big.NewInt(1), GasLimit: 30000000}, order: validated, signer: address, receipts: map[common.Hash]*types.Receipt{}}
		backends[chain.ID] = backend
		server := rpc.NewServer()
		if err = server.RegisterName("eth", backend); err != nil {
			t.Fatal(err)
		}
		httpServer := httptest.NewServer(server)
		defer httpServer.Close()
		client, err := evm.NewClient(t.Context(), []string{httpServer.URL}, chain.ID, 1000)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		clients[chain.ID] = client
		senders[chain.ID] = &evm.Sender{Client: client, Store: store, Signer: signer, Policy: evm.SendPolicy{Enabled: true, Chain: chain.ID, Confirmations: 2, MaxGas: 1000000, MaxFee: big.NewInt(100)}}
	}
	proofRequests, proofQueries := 0, 0
	proofServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		result := `{"status":"complete","proof":"AQID"}`
		if req.Method == "polymer_requestProof" {
			proofRequests++
			var logs []polymer.EVMLog
			if err := json.Unmarshal(req.Params, &logs); err != nil {
				t.Error(err)
				return
			}
			if len(logs) != 1 || logs[0].Index != 7 {
				t.Error("not using global log index")
			}
			result = "42"
		} else {
			proofQueries++
			if proofQueries == 1 {
				result = `{"status":"pending"}`
			}
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, result)
	}))
	defer proofServer.Close()
	proofs, err := polymer.New(proofServer.URL, "test", "polymer_requestProof", "polymer_queryProof", 1000)
	if err != nil {
		t.Fatal(err)
	}
	stages := []intent.Stage{}
	for range 24 {
		record, err := store.Record(t.Context(), validated.ID.Hex())
		if err != nil {
			t.Fatal(err)
		}
		stages = append(stages, record.Stage)
		if record.Stage == "settled" {
			break
		}
		lease, err := store.Acquire(t.Context(), "order:"+record.ID, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		// A new backend and engine receive only persisted progress after each step.
		backend, err := polymerevm.NewBackend(c.Routes[0].Settlement, c.Routes[0], address, clients, senders[c.Routes[0].OriginChain], proofs)
		if err != nil {
			t.Fatal(err)
		}
		engine := Engine{Verifier: testRouteVerifier{}, Config: c, Store: store, Clients: clients, Senders: map[string]map[uint64]*evm.Sender{c.Signers[0].Name: senders}, Settlements: map[string]settlement.Backend{c.Routes[0].Name: backend}, Execute: true}
		err = engine.Step(t.Context(), lease, record)
		if err := store.Release(context.Background(), lease); err != nil {
			t.Fatal(err)
		}
		if err != nil && !errors.Is(err, evm.ErrPending) {
			t.Fatalf("stage %s: %v", record.Stage, err)
		}
	}
	record, err := store.Record(t.Context(), validated.ID.Hex())
	if err != nil || record.Stage != "settled" {
		t.Fatalf("lifecycle incomplete %v %v", stages, err)
	}
	if backends[84532].fills != 1 || backends[11155111].claims != 1 || proofRequests != 1 || proofQueries != 2 {
		t.Fatalf("duplicate effects: fills %d claims %d requests %d queries %d", backends[84532].fills, backends[11155111].claims, proofRequests, proofQueries)
	}
}
