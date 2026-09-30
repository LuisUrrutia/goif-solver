package polymer

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequestPollAndDecodeProof(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		calls++
		var result string
		switch calls {
		case 1:
			if req.Method != "polymer_requestProof" || string(req.Params) != `[{"srcChainId":84532,"srcBlockNumber":42,"globalLogIndex":9}]` {
				t.Errorf("request: %+v", req)
			}
			result = `123`
		case 2:
			result = `{"status":"pending"}`
		default:
			result = `{"status":"complete","proof":"AQID"}`
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, result)
	}))
	defer server.Close()
	c, e := New(server.URL, "test-key", "polymer_requestProof", "polymer_queryProof", 1000)
	if e != nil {
		t.Fatal(e)
	}
	job, e := c.RequestEVM(t.Context(), EVMLog{ChainID: 84532, BlockNumber: 42, Index: 9})
	if e != nil || job != 123 {
		t.Fatalf("request %d: %v", job, e)
	}
	if _, e = c.Query(t.Context(), job); !errors.Is(e, ErrPending) {
		t.Fatal(e)
	}
	proof, e := c.Query(t.Context(), job)
	if e != nil || string(proof) != string([]byte{1, 2, 3}) {
		t.Fatalf("proof %x %v", proof, e)
	}
}

func TestQueryDistinguishesTerminalFailureFromUncertainty(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result string
		failed bool
	}{
		{"terminal", `{"status":"error","failureReason":"source block not available"}`, true},
		{"pending", `{"status":"pending"}`, false},
		{"unknown", `{"status":"new-status"}`, false},
		{"missing job", `{"status":"not_found"}`, false},
		{"malformed proof", `{"status":"complete","proof":"invalid"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req struct{ ID uint64 }
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, tc.result)
			}))
			defer server.Close()
			client, err := New(server.URL, "", "request", "query", 1000)
			if err != nil {
				t.Fatal(err)
			}

			_, err = client.Query(t.Context(), 42)

			var failure *JobError
			if errors.As(err, &failure) != tc.failed {
				t.Fatalf("terminal classification: %v", err)
			}
			if tc.failed && (failure.JobID != 42 || failure.Reason != "source block not available" || strings.Contains(err.Error(), failure.Reason)) {
				t.Fatal("failure lost its identity/reason or exposed provider text in logs", failure)
			}
		})
	}
}

func TestQueryBoundsFailureDiagnostics(t *testing.T) {
	reason := strings.Repeat("x", 4096)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ ID uint64 }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"status":"error","failureReason":%q}}`, req.ID, reason)
	}))
	defer server.Close()
	client, err := New(server.URL, "", "request", "query", 1000)
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Query(t.Context(), 42)

	var failure *JobError
	if !errors.As(err, &failure) || len(failure.Reason) != maxFailureReasonBytes {
		t.Fatal("unbounded provider diagnostic", err)
	}
}
