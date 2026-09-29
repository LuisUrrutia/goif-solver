package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	protocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"
	"go.uber.org/zap"
)

func TestPreflightChecksOnlyActiveNetworks(t *testing.T) {
	c, err := config.Load("../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	c.Providers, c.Publications, c.Sources = nil, nil, nil
	c.Development = true
	d := deployment(t, c)
	for i, chain := range d.Chains {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				return
			}
			result := "0x"
			switch request.Method {
			case "eth_chainId":
				result = fmt.Sprintf("0x%x", chain.ID)
			case "eth_blockNumber":
				result = "0x2a"
			}
			if err := json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
				t.Error(err)
			}
		}))
		t.Cleanup(server.Close)
		d.Chains[i].RPCs = []evm.Endpoint{{URL: server.URL, RequestsPerSecond: 1000}}
	}
	unused := d.Chains[0]
	unused.ID = 1337
	unused.RPCs = []evm.Endpoint{{Env: "UNUSED_PREFLIGHT_RPC"}}
	t.Setenv("UNUSED_PREFLIGHT_RPC", "")
	d.Chains = append([]evm.Chain{unused}, d.Chains...)
	setDeployment(t, &c, d)
	runtime, err := Open(t.Context(), c, nil, false, zap.NewNop(), builtins())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	report, err := runtime.Executions[protocol.IntentKind].Checker.Check(t.Context())

	if err == nil {
		t.Fatal("expected route verification to reject the fixture's absent contract code")
	}
	if len(report.Chains) != 2 {
		t.Fatalf("active networks: %+v", report.Chains)
	}
	for _, chain := range report.Chains {
		if chain.Network == "eip155:1337" || chain.Height != 42 {
			t.Fatalf("unexpected network report: %+v", chain)
		}
	}
}
