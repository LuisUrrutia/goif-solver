package polymerevm

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

type incompatibleOracle struct{}

func (incompatibleOracle) GetCode(common.Address, string) hexutil.Bytes { return []byte{0x60, 0x00} }

func TestVerifyRejectsIncompatibleOracle(t *testing.T) {
	_, route, signer := pilot(t)
	route.Settlement = "test-polymer"
	server := rpc.NewServer()
	if err := server.RegisterName("eth", incompatibleOracle{}); err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	client, err := ethclient.DialContext(t.Context(), httpServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	backend, err := NewBackend(route.Settlement, route, signer, map[uint64]*ethclient.Client{
		route.OriginChain: client, route.DestinationChain: client,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if err = backend.Verify(t.Context()); err == nil {
		t.Fatal("accepted incompatible oracle runtime")
	}
}

func TestAdvanceRejectsMismatchedLeaseAndCheckpointBeforeRPC(t *testing.T) {
	envelope, route, signer := pilot(t)
	validated, err := evm.Validate(envelope, route, signer, time.Unix(1790619000, 0))
	if err != nil {
		t.Fatal(err)
	}
	event := evm.OutputABI.Events["OutputFilled"]
	solver := evm.AddressWord(signer)
	const timestamp = uint32(1790619040)
	data, err := event.Inputs.NonIndexed().Pack(solver, timestamp, validated.Order.Outputs[0], validated.Order.Outputs[0].Amount)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := evm.SettlementEvidence(validated, evm.FillEvent{
		Solver: solver, Timestamp: timestamp,
		Log: types.Log{Address: route.OutputSettler, Topics: []common.Hash{event.ID, validated.ID}, Data: data},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = evm.DecodeFulfillment(evidence, route, signer); err != nil {
		t.Fatal(err)
	}
	request := settlement.Request{
		IntentID: (intent.Identity{Kind: evm.IntentKind, NativeID: validated.ID.Hex()}).Key(), Evidence: evidence,
		Lease: coordination.Lease{Resource: coordination.IntentResource((intent.Identity{Kind: evm.IntentKind, NativeID: validated.ID.Hex()}).Key())},
	}
	backend := Backend{route: route, signer: signer}
	for _, tc := range []struct {
		name     string
		state    string
		resource string
	}{
		{"foreign lease", "", coordination.IntentResource("another-intent")},
		{"unknown version", `{"version":2,"job":42}`, request.Lease.Resource},
		{"missing job", `{"version":1}`, request.Lease.Resource},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request
			r.Lease.Resource = tc.resource
			if _, err := backend.Advance(t.Context(), r, []byte(tc.state)); err == nil {
				t.Fatal("accepted invalid settlement request")
			}
		})
	}
}
