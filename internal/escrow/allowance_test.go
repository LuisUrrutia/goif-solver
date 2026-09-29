package escrow

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	protocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/storage/memorystore"
	"github.com/LuisUrrutia/goif-solver/internal/storage/redisstore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/redis/go-redis/v9"
)

type allowanceChain struct {
	*routeChain
	allowance *big.Int
	filledIDs map[common.Hash]bool
	approvals int
}

func allowanceCallData(call map[string]json.RawMessage) (hexutil.Bytes, error) {
	var data hexutil.Bytes
	input := call["input"]
	if len(input) == 0 {
		input = call["data"]
	}
	err := json.Unmarshal(input, &data)
	return data, err
}

func (c *allowanceChain) Call(call map[string]json.RawMessage, block string) (hexutil.Bytes, error) {
	data, err := allowanceCallData(call)
	if err != nil {
		return nil, err
	}
	if method, err := evm.TokenABI.MethodById(data); err == nil && method.RawName == "allowance" {
		c.mu.Lock()
		defer c.mu.Unlock()
		return method.Outputs.Pack(new(big.Int).Set(c.allowance))
	}
	return c.routeChain.Call(call, block)
}

func (c *allowanceChain) EstimateGas(call map[string]json.RawMessage) (hexutil.Uint64, error) {
	data, err := allowanceCallData(call)
	if err != nil {
		return 0, err
	}
	if method, err := protocol.OutputABI.MethodById(data); err == nil && method.RawName == "fillOrderOutputs" {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.allowance.Cmp(c.order.Order.Outputs[0].Amount) < 0 {
			return 0, errors.New("ERC20: insufficient allowance")
		}
	}
	return 100000, nil
}

func (c *allowanceChain) SendRawTransaction(raw hexutil.Bytes) (common.Hash, error) {
	var tx types.Transaction
	if err := tx.UnmarshalBinary(raw); err != nil {
		return common.Hash{}, err
	}
	c.mu.Lock()
	_, exists := c.receipts[tx.Hash()]
	c.mu.Unlock()
	hash, err := c.routeChain.SendRawTransaction(raw)
	if err != nil || exists {
		return hash, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if method, err := evm.TokenABI.MethodById(tx.Data()); err == nil && method.RawName == "approve" {
		args, err := method.Inputs.Unpack(tx.Data()[4:])
		if err != nil {
			return common.Hash{}, err
		}
		c.allowance.Set(args[1].(*big.Int))
		c.approvals++
	}
	if method, err := protocol.OutputABI.MethodById(tx.Data()); err == nil && method.RawName == "fillOrderOutputs" {
		c.allowance.Sub(c.allowance, c.order.Order.Outputs[0].Amount)
		c.filledIDs[c.order.ID] = true
	}
	return hash, nil
}

func TestApprovalOwnerKeepsAllowanceAcrossWorkerReplacement(t *testing.T) {
	for _, backend := range []string{"memory", "redis"} {
		t.Run(backend, func(t *testing.T) {
			var store coordination.Backend = memorystore.New()
			if backend == "redis" {
				address := os.Getenv("TEST_REDIS_ADDR")
				if address == "" {
					t.Skip("run scripts/check.sh for real Redis tests")
				}
				client := redis.NewClient(&redis.Options{Addr: address})
				t.Cleanup(func() { _ = client.Close() })
				var err error
				store, err = redisstore.New(client, fmt.Sprintf("allowance-%d", time.Now().UnixNano()))
				if err != nil {
					t.Fatal(err)
				}
			}
			policy, err := loadTestPolicy("../../config/testnet.json")
			if err != nil {
				t.Fatal(err)
			}
			key, err := crypto.GenerateKey()
			if err != nil {
				t.Fatal(err)
			}
			address := crypto.PubkeyToAddress(key.PublicKey)
			policy.Signers[0].Address = address
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
			orders := make([]protocol.Validated, 3)
			leases := make([]coordination.Lease, 3)
			for i := range orders {
				envelope.Order.Nonce = strconv.Itoa(i + 1)
				order, err := protocol.Parse(envelope.Intent().Order)
				if err != nil {
					t.Fatal(err)
				}
				id, err := protocol.Identifier(order, policy.Routes[0].InputSettler)
				if err != nil {
					t.Fatal(err)
				}
				orders[i] = protocol.Validated{ID: id, Order: order, Route: policy.Routes[0]}
				wire := protocol.Canonical(orders[i])
				if _, err = protocol.Validate(wire, policy.Routes[0], address, time.Now()); err != nil {
					t.Fatal(err)
				}
				payload, _ := json.Marshal(Work{Settlement: policy.Routes[0].Settlement, Version: policy.Version, Route: policy.Routes[0].Name, Envelope: wire})
				identity := (intent.Identity{Kind: protocol.IntentKind, NativeID: id.Hex()}).Key()
				if _, err = store.Enqueue(t.Context(), identity, string(payload)); err != nil {
					t.Fatal(err)
				}
				leases[i], err = store.Acquire(t.Context(), coordination.IntentResource(identity), time.Minute)
				if err != nil {
					t.Fatal(err)
				}
			}
			clients := make(map[uint64]*ethclient.Client)
			senders := make(map[uint64]*evm.Sender)
			chains := make(map[uint64]*allowanceChain)
			for _, chain := range policy.Chains {
				state := &allowanceChain{routeChain: &routeChain{chain: chain.ID, header: &types.Header{Number: big.NewInt(90), Difficulty: big.NewInt(0), BaseFee: big.NewInt(1), GasLimit: 30000000}, signer: address, receipts: make(map[common.Hash]*types.Receipt)}, allowance: new(big.Int), filledIDs: make(map[common.Hash]bool)}
				chains[chain.ID] = state
				server := rpc.NewServer()
				if err = server.RegisterName("eth", state); err != nil {
					t.Fatal(err)
				}
				remote := httptest.NewServer(server)
				t.Cleanup(remote.Close)
				client, err := evm.NewClient(t.Context(), []evm.RPCSettings{{URL: remote.URL}}, chain.ID, 1000)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(client.Close)
				clients[chain.ID] = client
				senders[chain.ID] = &evm.Sender{Client: client, Store: store, Signer: signer, Policy: evm.SendPolicy{Enabled: true, Chain: chain.ID, Confirmations: 2, MaxGas: 1000000, MaxFee: big.NewInt(100)}}
			}
			engine := &Engine{Verifier: testRouteVerifier{}, Store: store, Clients: clients, Senders: map[string]map[uint64]*evm.Sender{policy.Routes[0].Signer: senders}, Config: policy, Execute: true}
			record := func(i int) coordination.Record {
				id := (intent.Identity{Kind: protocol.IntentKind, NativeID: orders[i].ID.Hex()}).Key()
				r, err := store.Record(t.Context(), id)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			step := func(i int) error {
				for _, chain := range chains {
					chain.mu.Lock()
					chain.order = orders[i]
					chain.filled = chain.filledIDs[orders[i].ID]
					chain.mu.Unlock()
				}
				restarted := *engine
				return restarted.Step(t.Context(), leases[i], record(i))
			}
			reach := func(i int, stage intent.Stage) {
				t.Helper()
				for n := 0; n < 8 && record(i).Stage != stage; n++ {
					if err := step(i); err != nil && !errors.Is(err, evm.ErrPending) {
						t.Fatal(err)
					}
				}
				if got := record(i).Stage; got != stage {
					t.Fatalf("intent %d stopped at %s, want %s", i, got, stage)
				}
			}
			reach(0, Approved)
			reach(1, Validated)
			reach(2, Validated)
			for _, i := range []int{1, 2} {
				if err := step(i); !errors.Is(err, coordination.ErrBusy) || record(i).Stage != Validated {
					t.Fatalf("intent %d consumed another intent's allowance: stage=%s err=%v", i, record(i).Stage, err)
				}
			}
			if err := store.Release(t.Context(), leases[0]); err != nil {
				t.Fatal(err)
			}
			leases[0], err = store.Acquire(t.Context(), coordination.IntentResource(record(0).ID), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			reach(0, Filled)
			reach(1, Approved)
			if err := step(2); !errors.Is(err, coordination.ErrBusy) {
				t.Fatal("later intent bypassed the approval owner", err)
			}
			// An external allowance change still requires a new immutable attempt.
			destination := chains[policy.Routes[0].DestinationChain]
			destination.mu.Lock()
			destination.allowance.SetInt64(0)
			destination.mu.Unlock()
			reach(1, Filled)
			reach(2, Filled)

			destination.mu.Lock()
			allowance, approvals, fills := destination.allowance.String(), destination.approvals, destination.fills
			destination.mu.Unlock()
			if allowance != "0" || approvals != 4 || fills != 3 {
				t.Fatalf("allowance=%s approvals=%d fills=%d", allowance, approvals, fills)
			}
			var durable intent.Progress
			if err := json.Unmarshal([]byte(record(1).Detail), &durable); err != nil {
				t.Fatal(err)
			}
			var progress Progress
			if err := json.Unmarshal(durable.State, &progress); err != nil {
				t.Fatal(err)
			}
			if progress.ApprovalAttempt != 1 {
				t.Fatal("lost approval generation", progress.ApprovalAttempt)
			}
			resource := evm.SignerResource(policy.Routes[0].DestinationChain, address)
			first, err := store.Transaction(t.Context(), resource, record(1).ID+":approve")
			if err != nil {
				t.Fatal(err)
			}
			second, err := store.Transaction(t.Context(), resource, record(1).ID+":approve:1")
			if err != nil {
				t.Fatal(err)
			}
			if first.Hash == second.Hash {
				t.Fatal("reused a consumed approval")
			}
		})
	}
}
