package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/oif"
	protocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/storage/memorystore"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"go.uber.org/zap"
)

type quoteRPCTransport struct {
	t       *testing.T
	code    map[string]string
	stalled atomic.Int64
	calls   atomic.Int64
}

func (s *quoteRPCTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	s.calls.Add(1)
	defer func() { _ = r.Body.Close() }()
	if strings.HasSuffix(r.URL.Host, ".stalled.test") {
		s.stalled.Add(1)
		<-r.Context().Done()
		return nil, r.Context().Err()
	}
	select {
	case <-r.Context().Done():
		return nil, r.Context().Err()
	case <-time.After(250 * time.Millisecond):
	}
	var request struct {
		ID     json.RawMessage   `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		return nil, err
	}
	var result string
	switch request.Method {
	case "eth_chainId":
		result = "0x" + fmt.Sprintf("%x", uint64(84532))
		if strings.HasPrefix(r.URL.Host, "11155111.") {
			result = "0xaa36a7"
		}
	case "eth_getCode":
		var address string
		if err := json.Unmarshal(request.Params[0], &address); err != nil {
			return nil, err
		}
		result = s.code[strings.ToLower(address)]
	case "eth_call":
		var call struct {
			Input hexutil.Bytes `json:"input"`
		}
		if err := json.Unmarshal(request.Params[0], &call); err != nil {
			return nil, err
		}
		method, err := evm.TokenABI.MethodById(call.Input)
		if err != nil {
			return nil, err
		}
		switch method.RawName {
		case "decimals":
			result = fmt.Sprintf("0x%064x", 6)
		case "balanceOf":
			result = fmt.Sprintf("0x%064x", 100000000)
		default:
			s.t.Errorf("unexpected contract call %s", method.RawName)
		}
	default:
		s.t.Errorf("unexpected RPC method %s", request.Method)
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
}

func TestFirstQuoteSurvivesTwoColdPrimaryRPCOutages(t *testing.T) {
	c, err := config.Load("../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	c.Providers, c.Publications = nil, nil
	c.Sources = c.Sources[1:]
	d := deployment(t, c)
	route := d.Routes[0]
	raw, err := os.ReadFile("testdata/escrow-runtimes.json")
	if err != nil {
		t.Fatal(err)
	}
	var code map[string]string
	if err = json.Unmarshal(raw, &code); err != nil {
		t.Fatal(err)
	}
	for i := range d.Chains {
		chain := &d.Chains[i]
		chain.RequestsPerSecond = 4
		chain.RPCs = []evm.Endpoint{{URL: fmt.Sprintf("https://%d.stalled.test", chain.ID)}, {URL: fmt.Sprintf("https://%d.healthy.test", chain.ID)}}
	}
	setDeployment(t, &c, d)
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("corrupt-runtime-%t", corrupt), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				remote := &quoteRPCTransport{t: t, code: map[string]string{
					strings.ToLower(route.InputSettler.Hex()):  code["input"],
					strings.ToLower(route.OutputSettler.Hex()): code["output"],
					strings.ToLower(route.InputOracle.Hex()):   code["oracle"],
					strings.ToLower(route.OutputOracle.Hex()):  code["oracle"],
					strings.ToLower(route.InputToken.Hex()):    "0x6000",
					strings.ToLower(route.OutputToken.Hex()):   "0x6000",
				}}
				if corrupt {
					remote.code[strings.ToLower(route.OutputSettler.Hex())] = "0x6000"
				}
				previous := http.DefaultTransport
				http.DefaultTransport = remote
				defer func() { http.DefaultTransport = previous }()
				store := memorystore.New()
				runtime, err := Open(t.Context(), c, store, false, zap.NewNop(), builtins())
				if err != nil {
					t.Fatal(err)
				}
				defer runtime.Close()
				if remote.calls.Load() != 0 {
					t.Fatal("startup dialed unused networks")
				}
				const token = "synthetic-quote-failover-access-token"
				handler := &oif.Handler{Store: store, Routes: []oif.Route{runtime.Executions[protocol.IntentKind].OIF[route.Name]}, Token: token, QuoteKey: []byte(token), Provider: "test", Node: "test", RequestsPerSecond: 1000, Enabled: true}
				user := d.Signers[0].Address.Hex()
				query := oif.QuoteRequest{User: oif.Address{Chain: "eip155:11155111", Address: user}, SupportedTypes: []oif.OrderType{oif.UserOpen}, Intent: oif.Swap{IntentType: oif.SwapIntent, Inputs: []oif.Input{{Chain: "eip155:11155111", User: user, Asset: route.InputToken.Hex(), Amount: "1000000"}}, Outputs: []oif.Output{{Chain: "eip155:84532", Receiver: user, Asset: route.OutputToken.Hex()}}}}
				body, err := json.Marshal(query)
				if err != nil {
					t.Fatal(err)
				}

				for attempt := range 2 {
					before := remote.calls.Load()
					req := httptest.NewRequest(http.MethodPost, "/v1/quotes", bytes.NewReader(body))
					req.Header.Set("Authorization", "Bearer "+token)
					response := httptest.NewRecorder()
					start := time.Now()
					handler.HTTP().ServeHTTP(response, req)
					t.Logf("quote %d: HTTP %d after %s", attempt, response.Code, time.Since(start))

					want := http.StatusOK
					if corrupt {
						want = http.StatusServiceUnavailable
					}
					if response.Code != want {
						t.Fatalf("quote %d: HTTP %d after %s, want %d: %s", attempt, response.Code, time.Since(start), want, response.Body.String())
					}
					if !corrupt && time.Since(start) >= 8*time.Second {
						t.Fatal("exhausted the quote budget")
					}
					if !corrupt && attempt == 1 && remote.calls.Load()-before != 1 {
						t.Fatal("warm quote repeated verification instead of only checking inventory")
					}
				}
				if remote.stalled.Load() != 2 {
					t.Fatal("did not fail over both networks once", remote.stalled.Load())
				}
			})
		})
	}
}
