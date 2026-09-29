package polymer

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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
