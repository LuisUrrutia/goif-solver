package evmpreflight_test

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	evmpreflight "github.com/LuisUrrutia/goif-solver/internal/preflight/evm"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

type historicalRPC struct {
	id      common.Hash
	receipt json.RawMessage
}

func (r historicalRPC) Call(input map[string]json.RawMessage, _ string) (hexutil.Bytes, error) {
	var data hexutil.Bytes
	value := input["input"]
	if value == nil {
		value = input["data"]
	}
	if err := json.Unmarshal(value, &data); err != nil {
		return nil, err
	}
	method, err := escrowprotocol.InputABI.MethodById(data)
	if err != nil {
		return nil, err
	}
	if method.RawName == "orderIdentifier" {
		return method.Outputs.Pack([32]byte(r.id))
	}
	return method.Outputs.Pack(uint8(escrowprotocol.EscrowClaimed))
}

func (r historicalRPC) GetTransactionReceipt(common.Hash) json.RawMessage { return r.receipt }

type inspectionSpy struct{ called bool }

func (*inspectionSpy) Verify(context.Context) error { return nil }
func (*inspectionSpy) Advance(context.Context, settlement.Request, json.RawMessage) (settlement.Result, error) {
	return settlement.Result{}, nil
}

func (s *inspectionSpy) Inspect(context.Context, settlement.Evidence) (settlement.Verification, error) {
	s.called = true
	return settlement.Verification{Verified: true}, nil
}

func TestHistoricalRouteMatchesTokensBeforeSelectingBackend(t *testing.T) {
	root, err := config.Load("../../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := config.Decode[escrowprotocol.Deployment](root.Executions[escrowprotocol.IntentKind])
	if err != nil {
		t.Fatal(err)
	}
	correct := c.Routes[0]
	wrong := correct
	wrong.Name = "different-token"
	wrong.InputToken = common.HexToAddress("0x123")
	c.Routes = []escrowprotocol.Route{wrong, correct}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	orderJSON, err := os.ReadFile("../../../internal/lifi/testdata/pilot-order.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope lifi.Envelope
	if err = json.Unmarshal(orderJSON, &envelope); err != nil {
		t.Fatal(err)
	}
	fillJSON, err := os.ReadFile("../../../internal/protocol/escrow/testdata/pilot-fill.json")
	if err != nil {
		t.Fatal(err)
	}
	var fill struct {
		Log types.Log `json:"log"`
	}
	if err = json.Unmarshal(fillJSON, &fill); err != nil {
		t.Fatal(err)
	}
	receipt, err := json.Marshal(&types.Receipt{Type: 2, Status: 1, Logs: []*types.Log{&fill.Log}, TxHash: fill.Log.TxHash, BlockHash: fill.Log.BlockHash, BlockNumber: new(big.Int).SetUint64(fill.Log.BlockNumber)})
	if err != nil {
		t.Fatal(err)
	}
	rpcServer := rpc.NewServer()
	if err = rpcServer.RegisterName("eth", historicalRPC{id: common.HexToHash(envelope.Meta.ID), receipt: receipt}); err != nil {
		t.Fatal(err)
	}
	remote := httptest.NewServer(http.HandlerFunc(rpcServer.ServeHTTP))
	defer remote.Close()
	client, err := ethclient.DialContext(t.Context(), remote.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	wrongBackend, correctBackend := &inspectionSpy{}, &inspectionSpy{}
	clients := map[uint64]*ethclient.Client{correct.OriginChain: client, correct.DestinationChain: client}
	backends := map[string]settlement.Backend{wrong.Name: wrongBackend, correct.Name: correctBackend}

	report, err := evmpreflight.AuditIntent(t.Context(), c, envelope.Intent(), envelope.Meta.Status, envelope.Meta.FillTx, clients, backends)

	if err != nil || report.Route != correct.Name || wrongBackend.called || !correctBackend.called {
		t.Fatalf("unexpected route selection: report=%+v err=%v wrong=%v correct=%v", report, err, wrongBackend.called, correctBackend.called)
	}
}
