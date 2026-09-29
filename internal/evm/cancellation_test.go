package evm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/storage/memorystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rpc"
)

func TestRPCOperationsPreserveCancellation(t *testing.T) {
	tx := types.NewTx(&types.LegacyTx{Gas: 21000})
	receipt := func(ctx context.Context, sender *Sender) error { _, err := sender.receipt(ctx, tx); return err }
	execute := func(ctx context.Context, sender *Sender) error {
		_, err := sender.Execute(ctx, coordination.Lease{}, SendRequest{Operation: "test:fill"})
		return err
	}
	for _, test := range []struct {
		name, method, tag string
		run               func(context.Context, *Sender) error
		mined             bool
	}{
		{name: "contract", method: "eth_call", run: func(ctx context.Context, sender *Sender) error {
			_, err := Balance(ctx, sender.Client, common.Address{}, sender.Signer.Address())
			return err
		}},
		{name: "receipt", method: "eth_getTransactionReceipt", run: receipt, mined: true},
		{name: "receipt block", method: "eth_getBlockByNumber", run: receipt, mined: true},
		{name: "head", method: "eth_blockNumber", run: receipt, mined: true},
		{name: "pending nonce", method: "eth_getTransactionCount", tag: "pending", run: execute},
		{name: "mined nonce", method: "eth_getTransactionCount", tag: "latest", run: execute},
		{name: "gas tip", method: "eth_maxPriorityFeePerGas", run: execute},
		{name: "gas header", method: "eth_getBlockByNumber", run: execute},
		{name: "simulation", method: "eth_estimateGas", run: execute},
		{name: "gas balance", method: "eth_getBalance", run: execute},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			backend := &chainRPC{header: &types.Header{Number: big.NewInt(90), Difficulty: big.NewInt(0), BaseFee: big.NewInt(1)}, tx: tx, mined: test.mined}
			server := rpc.NewServer()
			if err := server.RegisterName("eth", backend); err != nil {
				t.Fatal(err)
			}
			var interrupted atomic.Bool
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				_ = r.Body.Close()
				if err != nil {
					t.Error(err)
					return
				}
				var request struct {
					Method string            `json:"method"`
					Params []json.RawMessage `json:"params"`
				}
				if err := json.Unmarshal(body, &request); err != nil {
					t.Error(err)
					return
				}
				tagMatches := test.tag == ""
				if !tagMatches && len(request.Params) > 1 {
					var tag string
					tagMatches = json.Unmarshal(request.Params[1], &tag) == nil && tag == test.tag
				}
				if request.Method == test.method && tagMatches {
					interrupted.Store(true)
					cancel()
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
				server.ServeHTTP(w, r)
			}))
			defer remote.Close()
			client, err := NewClient(t.Context(), []RPCSettings{{URL: remote.URL}}, 84532, 1000)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			key, err := crypto.GenerateKey()
			if err != nil {
				t.Fatal(err)
			}
			signer, err := NewLocalSigner(hexutil.Encode(crypto.FromECDSA(key)), crypto.PubkeyToAddress(key.PublicKey), []uint64{84532})
			if err != nil {
				t.Fatal(err)
			}
			sender := &Sender{Client: client, Store: memorystore.New(), Signer: signer, Policy: SendPolicy{Enabled: true, Chain: 84532, MaxGas: 100000, MaxFee: big.NewInt(100)}}
			deadline, stop := context.WithTimeout(ctx, 5*time.Second)
			defer stop()

			err = test.run(deadline, sender)

			if !interrupted.Load() || !errors.Is(err, context.Canceled) {
				t.Fatalf("operation did not retain RPC cancellation: interrupted=%t error=%v", interrupted.Load(), err)
			}
		})
	}
}
