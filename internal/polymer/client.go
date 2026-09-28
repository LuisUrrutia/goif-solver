// Package polymer requests and polls log proofs without handling signing keys.
package polymer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"

	"github.com/LuisUrrutia/goif-solver/internal/transport"
)

var ErrPending = errors.New("proof pending")

type Client struct {
	http                       *transport.Client
	sequence                   atomic.Uint64
	requestMethod, queryMethod string
}
type Log struct {
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
		JSONRPC string      `json:"jsonrpc"`
		ID      uint64      `json:"id"`
		Method  string      `json:"method"`
		Params  interface{} `json:"params"`
	}{"2.0", id, method, params}
	var res struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      uint64          `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *struct {
			Code int `json:"code"`
		} `json:"error"`
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
func (c *Client) Request(ctx context.Context, log Log) (uint64, error) {
	var id uint64
	e := c.call(ctx, c.requestMethod, []Log{log}, &id)
	if e == nil && id == 0 {
		e = errors.New("empty proof job")
	}
	return id, e
}
func (c *Client) Query(ctx context.Context, id uint64) ([]byte, error) {
	var out struct {
		Status string `json:"status"`
		Proof  string `json:"proof"`
	}
	if e := c.call(ctx, c.queryMethod, []uint64{id}, &out); e != nil {
		return nil, e
	}
	switch out.Status {
	case "queued", "pending", "processing":
		return nil, ErrPending
	case "complete", "completed":
	default:
		return nil, errors.New("proof job failed or returned unknown status")
	}
	b, e := base64.StdEncoding.DecodeString(out.Proof)
	if e != nil || len(b) == 0 || len(b) > 1<<20 {
		return nil, errors.New("invalid proof bytes")
	}
	return b, nil
}
