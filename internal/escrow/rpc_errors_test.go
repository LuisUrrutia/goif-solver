package escrow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/intent"
	protocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/transport"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

type failingExecutionRPC struct {
	cause  error
	chain  *routeChain
	method string
}

func (r failingExecutionRPC) RoundTrip(req *http.Request) (*http.Response, error) {
	defer func() { _ = req.Body.Close() }()
	var request struct {
		ID     json.RawMessage   `json:"id"`
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
	}
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		return nil, err
	}
	if request.Method == r.method {
		return nil, fmt.Errorf("https://rpc.invalid/synthetic-secret: %w", r.cause)
	}
	if request.Method != "eth_call" {
		return nil, fmt.Errorf("unexpected RPC method %s", request.Method)
	}
	var call map[string]json.RawMessage
	if err := json.Unmarshal(request.Params[0], &call); err != nil {
		return nil, err
	}
	result, err := r.chain.Call(call, "latest")
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": hexutil.Encode(result)})
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: req}, nil
}

func TestEscrowClassifiesFinalityAndReconciliationFailures(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, transport.ErrUnavailable} {
		for _, method := range []string{"eth_blockNumber", "eth_getBlockByNumber"} {
			t.Run(method+"/"+cause.Error(), func(t *testing.T) {
				x, _ := filledExecution(t, nil)
				envelope := &x.work.Envelope
				envelope.Order.FillDeadline = strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
				envelope.Order.Expires = strconv.FormatInt(time.Now().Add(24*time.Hour).Unix(), 10)
				envelope.Order.Outputs[0].Context = "0x"
				var err error
				x.v, err = protocol.Validate(*envelope, x.v.Route, x.e.Config.Signers[0].Address, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				remote := failingExecutionRPC{cause: cause, method: method, chain: &routeChain{order: x.v}}
				connection, err := rpc.DialOptions(t.Context(), "http://127.0.0.1", rpc.WithHTTPClient(&http.Client{Transport: remote}))
				if err != nil {
					t.Fatal(err)
				}
				client := ethclient.NewClient(connection)
				t.Cleanup(client.Close)
				x.origin = client
				x.e.Clients = map[uint64]*ethclient.Client{x.v.Route.OriginChain: client, x.v.Route.DestinationChain: client}
				state, err := json.Marshal(x.progress)
				if err != nil {
					t.Fatal(err)
				}
				detail, err := json.Marshal(intent.Progress{State: state})
				if err != nil {
					t.Fatal(err)
				}
				x.record.Detail = string(detail)

				if method == "eth_blockNumber" {
					err = x.validate()
				} else {
					err = x.e.Step(t.Context(), x.lease, x.record)
				}

				if !errors.Is(err, cause) || strings.Contains(err.Error(), "synthetic-secret") {
					t.Fatalf("RPC error lost classification or leaked details: %v", err)
				}
			})
		}
	}
}
