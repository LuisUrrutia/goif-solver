package app

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"
	polymerevm "github.com/LuisUrrutia/goif-solver/internal/settlement/polymer/evm"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"go.uber.org/zap"
)

func TestDurableOnChainHistoryDoesNotNeedAnOrderProvider(t *testing.T) {
	c, err := config.Load("../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	c.Providers = nil
	c.Publications = nil
	c.Sources = c.Sources[1:]
	raw, err := os.ReadFile("../lifi/testdata/pilot-order.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope lifi.Envelope
	if err = json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../protocol/escrow/testdata/pilot-fill.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Log types.Log `json:"log"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	receipt := &types.Receipt{Type: 2, Status: 1, Logs: []*types.Log{&fixture.Log}, TxHash: fixture.Log.TxHash, BlockHash: fixture.Log.BlockHash, BlockNumber: new(big.Int).SetUint64(fixture.Log.BlockNumber)}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     uint64            `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		var result any
		switch request.Method {
		case "eth_chainId":
			result = r.URL.Path[1:]
		case "eth_getTransactionReceipt":
			result = receipt
		case "eth_call":
			var args map[string]hexutil.Bytes
			if err := json.Unmarshal(request.Params[0], &args); err != nil {
				t.Error(err)
				return
			}
			data := args["input"]
			if data == nil {
				data = args["data"]
			}
			method, err := escrowprotocol.InputABI.MethodById(data)
			var packed []byte
			if err == nil {
				if method.RawName == "orderIdentifier" {
					packed, err = method.Outputs.Pack([32]byte(common.HexToHash(envelope.Meta.ID)))
				} else {
					packed, err = method.Outputs.Pack(uint8(escrowprotocol.EscrowClaimed))
				}
			} else {
				method, err = polymerevm.OracleABI.MethodById(data)
				if err == nil {
					packed, err = method.Outputs.Pack(true)
				}
			}
			if err != nil {
				t.Error(err)
				return
			}
			result = hexutil.Encode(packed)
		default:
			t.Error("unexpected RPC", request.Method)
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			t.Error(err)
		}
	}))
	defer remote.Close()
	d := deployment(t, c)
	for i := range d.Chains {
		d.Chains[i].RPCs = []evm.Endpoint{{URL: fmt.Sprintf("%s/0x%x", remote.URL, d.Chains[i].ID), RequestsPerSecond: 1000}}
	}
	setDeployment(t, &c, d)
	runtime, err := Open(t.Context(), c, nil, false, zap.NewNop(), builtins())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	order, err := escrowprotocol.Parse(envelope.Intent().Order)
	if err != nil {
		t.Fatal(err)
	}
	fill, err := escrowprotocol.DecodeFill(receipt, escrowprotocol.Validated{ID: common.HexToHash(envelope.Meta.ID), Order: order, Route: d.Routes[0]}, d.Signers[0].Address)
	if err != nil {
		t.Fatal(err)
	}
	candidate := intent.Candidate{Kind: escrowprotocol.IntentKind, ID: envelope.Meta.ID, Payload: encodeSettings(t, escrow.Work{Route: d.Routes[0].Name, Settlement: d.Routes[0].Settlement, Envelope: envelope.Intent(), Version: c.Version})}
	record := coordination.Record{ID: candidate.Identity().Key(), Payload: string(encodeSettings(t, candidate)), Stage: intent.Settled, Detail: string(encodeSettings(t, intent.Progress{State: encodeSettings(t, escrow.Progress{Fill: &fill})}))}

	report, err := runtime.AuditRecord(t.Context(), record, candidate.Identity(), false)

	if err != nil || report.ID != envelope.Meta.ID || !report.Verification.Verified {
		t.Fatal(report, err)
	}
	if len(runtime.Providers) != 0 {
		t.Fatal("initialized an order provider")
	}
}
