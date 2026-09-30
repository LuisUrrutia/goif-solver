package polymerevm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"github.com/LuisUrrutia/goif-solver/internal/settlement/polymer"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

type proofOracle struct{ verified atomic.Bool }

func (o *proofOracle) Call(context.Context, map[string]any, string) (hexutil.Bytes, error) {
	return OracleABI.Methods["isProven"].Outputs.Pack(o.verified.Load())
}

func proofBackend(t *testing.T, reply func(string, json.RawMessage) string) (func(RetryPolicy) *Backend, settlement.Request, *proofOracle) {
	t.Helper()
	envelope, route, signer := pilot(t)
	route.Settlement = "polymer-test"
	parsed, err := escrowprotocol.ParseIntent(envelope)
	if err != nil {
		t.Fatal(err)
	}
	v, err := parsed.Validate(route, signer, time.Unix(1790619000, 0))
	if err != nil {
		t.Fatal(err)
	}
	event := escrowprotocol.OutputABI.Events["OutputFilled"]
	const timestamp = uint32(1790619040)
	data, err := event.Inputs.NonIndexed().Pack(evm.AddressWord(signer), timestamp, v.Order.Outputs[0], v.Order.Outputs[0].Amount)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := escrowprotocol.SettlementEvidence(v, escrowprotocol.FillEvent{
		Solver: evm.AddressWord(signer), Timestamp: timestamp,
		Log: types.Log{Address: route.OutputSettler, Topics: []common.Hash{event.ID, v.ID}, Data: data, BlockNumber: 100, Index: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := (intent.Identity{Kind: escrowprotocol.IntentKind, NativeID: v.ID.Hex()}).Key()
	request := settlement.Request{IntentID: id, Lease: coordination.Lease{Resource: coordination.IntentResource(id)}, Evidence: evidence}
	oracle := new(proofOracle)
	server := rpc.NewServer()
	if err := server.RegisterName("eth", oracle); err != nil {
		t.Fatal(err)
	}
	client := ethclient.NewClient(rpc.DialInProc(server))
	t.Cleanup(client.Close)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			ID     uint64          `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,%s}`, req.ID, reply(req.Method, req.Params))
	}))
	t.Cleanup(api.Close)
	return func(policy RetryPolicy) *Backend {
		proofs, err := polymer.New(api.URL, "", "request", "query", 1000)
		if err != nil {
			t.Fatal(err)
		}
		backend, err := NewBackend(route.Settlement, route, signer, map[uint64]*ethclient.Client{route.OriginChain: client, route.DestinationChain: client}, nil, proofs, policy)
		if err != nil {
			t.Fatal(err)
		}
		return backend
	}, request, oracle
}

func TestAdvanceReplacesFailedProofJobAfterRestart(t *testing.T) {
	var requests, queries atomic.Int32
	open, request, oracle := proofBackend(t, func(method string, params json.RawMessage) string {
		if method == "request" {
			requests.Add(1)
			if string(params) != `[{"srcChainId":84532,"srcBlockNumber":100,"globalLogIndex":2}]` {
				t.Error("replacement changed fill coordinates", string(params))
			}
			return `"result":43`
		}
		queries.Add(1)
		if string(params) == `[42]` {
			return `"result":{"status":"error","failureReason":"source block not available"}`
		}
		if string(params) != `[43]` {
			t.Error("queried unexpected job", string(params))
		}
		return `"result":{"status":"complete","proof":"AQID"}`
	})
	legacy := json.RawMessage(`{"version":1,"job":42}`)

	failed, err := open(RetryPolicy{}).Advance(t.Context(), request, legacy)

	if err != nil || failed.Status != settlement.Pending || failed.RetryAfter != 30*time.Second {
		t.Fatalf("terminal failure was not deferred: %+v, %v", failed, err)
	}
	var saved checkpoint
	if err := json.Unmarshal(failed.State, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Version != stateVersion || saved.Requests != 1 || saved.LastFailure == nil || saved.LastFailure.JobID != 42 || saved.LastFailure.Reason != "source block not available" {
		t.Fatalf("incomplete recovery checkpoint: %+v", saved)
	}
	// A crash before checkpoint persistence can only repeat the failed query.
	if _, err := open(RetryPolicy{}).Advance(t.Context(), request, legacy); err != nil || requests.Load() != 0 {
		t.Fatal("requested a replacement before persisting the failure", err)
	}
	replacement, err := open(RetryPolicy{}).Advance(t.Context(), request, failed.State)
	if err != nil || replacement.Status != settlement.Pending {
		t.Fatal("replacement request failed", err)
	}
	complete, err := open(RetryPolicy{}).Advance(t.Context(), request, replacement.State)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(complete.State, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Job != 43 || saved.Requests != 2 || string(saved.Proof) != string([]byte{1, 2, 3}) || requests.Load() != 1 || queries.Load() != 3 {
		t.Fatalf("replacement did not produce a durable proof: %+v requests=%d queries=%d", saved, requests.Load(), queries.Load())
	}
	oracle.verified.Store(true)
	verified, err := open(RetryPolicy{}).Advance(t.Context(), request, complete.State)
	if err != nil || verified.Status != settlement.Verified || requests.Load() != 1 || queries.Load() != 3 {
		t.Fatal("external verification did not finish settlement", err)
	}
}

func TestProofJobLimitSurvivesRestartsAndCanBeExtended(t *testing.T) {
	for _, duplicateID := range []bool{false, true} {
		t.Run(fmt.Sprintf("duplicate_id_%t", duplicateID), func(t *testing.T) {
			var requests, queries atomic.Int32
			open, request, oracle := proofBackend(t, func(method string, _ json.RawMessage) string {
				if method == "request" {
					id := requests.Add(1)
					if duplicateID {
						id = 1
					}
					return fmt.Sprintf(`"result":%d`, id)
				}
				queries.Add(1)
				return `"result":{"status":"error","failureReason":"generation failed"}`
			})
			var state json.RawMessage
			var err error
			for range 10 {
				var result settlement.Result
				result, err = open(RetryPolicy{}).Advance(t.Context(), request, state)
				if err != nil {
					break
				}
				state = result.State
				var saved checkpoint
				if err := json.Unmarshal(state, &saved); err != nil {
					t.Fatal(err)
				}
				if saved.LastFailure != nil && saved.LastFailure.JobID == saved.Job && result.RetryAfter <= 0 {
					t.Fatal("failed job replacement did not back off")
				}
			}
			if !errors.Is(err, ErrJobLimit) || requests.Load() != 3 {
				t.Fatalf("job limit lost across restarts: requests=%d err=%v", requests.Load(), err)
			}
			queryCount := queries.Load()
			for range 3 {
				if _, err := open(RetryPolicy{}).Advance(t.Context(), request, state); !errors.Is(err, ErrJobLimit) {
					t.Fatal("exhausted state resumed without intervention", err)
				}
			}
			if requests.Load() != 3 || queries.Load() != queryCount {
				t.Fatal("exhausted state kept calling Polymer")
			}
			if _, err := open(RetryPolicy{MaxJobs: 4}).Advance(t.Context(), request, state); err != nil || requests.Load() != 4 {
				t.Fatal("operator could not extend the persisted budget", err)
			}
			oracle.verified.Store(true)
			result, err := open(RetryPolicy{}).Advance(t.Context(), request, state)
			if err != nil || result.Status != settlement.Verified || requests.Load() != 4 {
				t.Fatal("exhaustion blocked on-chain reconciliation", err)
			}
		})
	}
}

func TestProofUncertaintyRetainsCurrentJob(t *testing.T) {
	for _, response := range []string{
		`"result":{"status":"pending"}`,
		`"result":{"status":"unknown"}`,
		`"result":{"status":"complete","proof":"invalid"}`,
		`"error":{"code":-32000}`,
	} {
		t.Run(response, func(t *testing.T) {
			var requests atomic.Int32
			open, request, _ := proofBackend(t, func(method string, params json.RawMessage) string {
				if method == "request" {
					requests.Add(1)
				}
				if string(params) != `[42]` {
					t.Error("uncertain job was replaced", string(params))
				}
				return response
			})
			state := json.RawMessage(`{"version":2,"job":42,"requests":1}`)

			for range 3 {
				result, err := open(RetryPolicy{}).Advance(t.Context(), request, state)
				if err == nil {
					state = result.State
				}
			}

			var saved checkpoint
			if err := json.Unmarshal(state, &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Requests != 1 || saved.Job != 42 || saved.LastFailure != nil || requests.Load() != 0 {
				t.Fatal("uncertainty consumed another proof request", string(state))
			}
		})
	}
}

func TestReplacementRequestFailureRetainsCheckpoint(t *testing.T) {
	var requests atomic.Int32
	open, request, _ := proofBackend(t, func(method string, _ json.RawMessage) string {
		if method != "request" {
			t.Error("queried a terminally failed job")
		}
		if requests.Add(1) == 1 {
			return `"error":{"code":-32000}`
		}
		return `"result":43`
	})
	state := json.RawMessage(`{"version":2,"job":42,"requests":2,"last_failure":{"job":42,"reason":"source block not available"}}`)

	result, err := open(RetryPolicy{}).Advance(t.Context(), request, state)
	if err == nil || len(result.State) != 0 {
		t.Fatal("request uncertainty changed the durable state", err)
	}
	result, err = open(RetryPolicy{}).Advance(t.Context(), request, state)
	if err != nil {
		t.Fatal(err)
	}
	var saved checkpoint
	if err := json.Unmarshal(result.State, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Job != 43 || saved.Requests != 3 || saved.LastFailure == nil || saved.LastFailure.JobID != 42 {
		t.Fatal("request failure reset the budget or lost the previous failure", string(result.State))
	}
}
