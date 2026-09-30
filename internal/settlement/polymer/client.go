// Package polymer implements the proof service wire protocol without chain clients or signers.
package polymer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"

	"github.com/LuisUrrutia/goif-solver/internal/transport"
)

var ErrPending = errors.New("proof pending")

const maxFailureReasonBytes = 512

type JobError struct {
	Reason string `json:"reason"`
	JobID  uint64 `json:"job"`
}

func (e *JobError) Error() string {
	// Provider text belongs in the diagnostic checkpoint, not in application logs.
	return fmt.Sprintf("polymer proof job %d failed", e.JobID)
}

type Client struct {
	http          *transport.Client
	requestMethod string
	queryMethod   string
	sequence      atomic.Uint64
}
type EVMLog struct {
	ChainID     uint64 `json:"srcChainId"`
	BlockNumber uint64 `json:"srcBlockNumber"`
	Index       uint   `json:"globalLogIndex"`
}

func New(base, key, requestMethod, queryMethod string, rps int) (*Client, error) {
	if requestMethod == "" || queryMethod == "" {
		return nil, errors.New("proof RPC methods required")
	}
	h := http.Header{}
	if key != "" {
		h.Set("Authorization", "Bearer "+key)
	}
	c, e := transport.New(base, h, rps)
	if e != nil {
		return nil, e
	}
	return &Client{http: c, requestMethod: requestMethod, queryMethod: queryMethod}, nil
}

func (c *Client) call(ctx context.Context, method string, params interface{}, out interface{}) error {
	id := c.sequence.Add(1)
	req := struct {
		Params  interface{} `json:"params"`
		JSONRPC string      `json:"jsonrpc"`
		Method  string      `json:"method"`
		ID      uint64      `json:"id"`
	}{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	var res struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
		JSONRPC string          `json:"jsonrpc"`
		Result  json.RawMessage `json:"result"`
		ID      uint64          `json:"id"`
	}
	if e := c.http.Do(ctx, http.MethodPost, "", req, &res); e != nil {
		return e
	}
	if res.ID != id || res.JSONRPC != "2.0" {
		return errors.New("mismatched proof RPC response")
	}
	if res.Error != nil {
		return errors.New("proof RPC error")
	}
	if e := json.Unmarshal(res.Result, out); e != nil {
		return errors.New("invalid proof RPC result")
	}
	return nil
}

func (c *Client) RequestEVM(ctx context.Context, log EVMLog) (uint64, error) {
	var id uint64
	e := c.call(ctx, c.requestMethod, []EVMLog{log}, &id)
	if e == nil && id == 0 {
		e = errors.New("empty proof job")
	}
	return id, e
}

func (c *Client) Query(ctx context.Context, id uint64) ([]byte, error) {
	var out struct {
		Status        string `json:"status"`
		Proof         string `json:"proof"`
		FailureReason string `json:"failureReason"`
	}
	if e := c.call(ctx, c.queryMethod, []uint64{id}, &out); e != nil {
		return nil, e
	}
	switch out.Status {
	case "queued", "pending", "processing", "initialized":
		return nil, ErrPending
	case "complete", "completed":
	case "error":
		return nil, &JobError{JobID: id, Reason: out.FailureReason[:min(len(out.FailureReason), maxFailureReasonBytes)]}
	default:
		return nil, errors.New("unknown proof job status")
	}
	b, e := base64.StdEncoding.DecodeString(out.Proof)
	if e != nil || len(b) == 0 || len(b) > 1<<20 {
		return nil, errors.New("invalid proof bytes")
	}
	return b, nil
}
