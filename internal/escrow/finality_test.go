package escrow

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	protocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/storage/memorystore"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

type finalityChain struct {
	*routeChain
	historyError error
	latest       uint8
	historical   uint8
}

func (c *finalityChain) Call(call map[string]json.RawMessage, block string) (hexutil.Bytes, error) {
	data, err := allowanceCallData(call)
	if err != nil {
		return nil, err
	}
	method, err := protocol.InputABI.MethodById(data)
	if err == nil && method.RawName == "orderStatus" {
		if block == "latest" {
			return method.Outputs.Pack(c.latest)
		}
		if c.historyError != nil {
			return nil, c.historyError
		}
		return method.Outputs.Pack(c.historical)
	}
	return c.routeChain.Call(call, block)
}

func TestOriginFinalitySchedulesNormalWaitWithoutAdvancing(t *testing.T) {
	for _, test := range []struct {
		name         string
		historyError error
		latest       uint8
		historical   uint8
		deferred     bool
		rejected     bool
		validated    bool
	}{
		{name: "pending confirmations", latest: 1, deferred: true},
		{name: "confirmed deposit", latest: 1, historical: 1, validated: true},
		{name: "unfunded"},
		{name: "deposit removed", historical: 1},
		{name: "claimed", latest: 2, rejected: true},
		{name: "refunded", latest: 3, rejected: true},
		{name: "inconsistent history", latest: 1, historical: 2, rejected: true},
		{name: "history unavailable", latest: 1, historyError: errors.New("historical RPC unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, err := loadTestPolicy("../../config/testnet.json")
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
			validated, err := protocol.Validate(envelope.Intent(), policy.Routes[0], policy.Signers[0].Address, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			chain := &finalityChain{routeChain: &routeChain{order: validated}, latest: test.latest, historical: test.historical, historyError: test.historyError}
			server := rpc.NewServer()
			if err = server.RegisterName("eth", chain); err != nil {
				t.Fatal(err)
			}
			remote := httptest.NewServer(server)
			t.Cleanup(remote.Close)
			client, err := ethclient.Dial(remote.URL)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(client.Close)
			store := memorystore.New()
			engine := Engine{Config: policy, Store: store, Clients: map[uint64]*ethclient.Client{policy.Routes[0].OriginChain: client}}
			payload, _ := json.Marshal(Work{Envelope: envelope.Intent(), Route: policy.Routes[0].Name, Settlement: policy.Routes[0].Settlement, Version: policy.Version})
			id := (intent.Identity{Kind: protocol.IntentKind, NativeID: validated.ID.Hex()}).Key()
			if _, err = store.Enqueue(t.Context(), id, string(payload)); err != nil {
				t.Fatal(err)
			}
			lease, err := store.Acquire(t.Context(), coordination.IntentResource(id), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			record, err := store.Record(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}

			err = engine.Step(t.Context(), lease, record)

			var deferred *intent.Deferred
			if errors.As(err, &deferred) != test.deferred || errors.Is(err, intent.ErrRejected) != test.rejected || (err == nil) != test.validated {
				t.Fatalf("unexpected finality result: %v", err)
			}
			if deferred != nil && (deferred.After <= 0 || deferred.After > 5*time.Second) {
				t.Fatalf("normal finality wait delayed by %s", deferred.After)
			}
			record, err = store.Record(t.Context(), id)
			if err != nil {
				t.Fatal(err)
			}
			if (record.Stage == Validated) != test.validated {
				t.Fatalf("advanced before finality: %s", record.Stage)
			}
		})
	}
}
