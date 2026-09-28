// Package lifi implements the LI.FI development order-server contract.
package lifi

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/LuisUrrutia/goif-solver/internal/transport"
)

type Order struct {
	User          string     `json:"user"`
	Nonce         string     `json:"nonce"`
	OriginChainID string     `json:"originChainId"`
	FillDeadline  string     `json:"fillDeadline"`
	Expires       string     `json:"expires"`
	InputOracle   string     `json:"inputOracle"`
	Inputs        [][]string `json:"inputs"`
	Outputs       []Output   `json:"outputs"`
}
type Output struct {
	Oracle       string `json:"oracle"`
	Settler      string `json:"settler"`
	Token        string `json:"token"`
	Amount       string `json:"amount"`
	Recipient    string `json:"recipient"`
	ChainID      string `json:"chainId"`
	CallbackData string `json:"callbackData"`
	Context      string `json:"context"`
}
type Envelope struct {
	Order        Order  `json:"order"`
	InputSettler string `json:"inputSettler"`
	Meta         struct {
		ID     string `json:"onChainOrderId"`
		Status string `json:"orderStatus"`
	} `json:"meta"`
}
type Page struct {
	Data []Envelope `json:"data"`
	Meta struct {
		Total  int `json:"total"`
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
	} `json:"meta"`
}
type Range struct {
	MinAmount   string       `json:"minAmount"`
	MaxAmount   string       `json:"maxAmount"`
	Quote       string       `json:"quote"`
	OracleCosts []OracleCost `json:"oracleCosts,omitempty"`
}
type OracleCost struct {
	InputOracle  string `json:"inputOracle"`
	OutputOracle string `json:"outputOracle"`
	FixedCost    string `json:"fixedCost"`
}
type Quote struct {
	FromChain    string  `json:"fromChain"`
	ToChain      string  `json:"toChain"`
	FromAsset    string  `json:"fromAsset"`
	ToAsset      string  `json:"toAsset"`
	FromDecimals int     `json:"fromDecimals"`
	ToDecimals   int     `json:"toDecimals"`
	Ranges       []Range `json:"ranges"`
	Expiry       int64   `json:"expiry"`
	ExclusiveFor string  `json:"exclusiveFor,omitempty"`
}
type Contract struct {
	Chain   string `json:"chain"`
	Address string `json:"address"`
}
type InputContract struct {
	Contract
	Type string `json:"type"`
}
type Catalog struct {
	InputSettlers  []InputContract `json:"inputSettlers"`
	OutputSettlers []Contract      `json:"outputSettlers"`
	Oracles        []struct {
		ID          string `json:"id"`
		Deployments []struct {
			Contracts []struct {
				Contract
				Status string `json:"status"`
			} `json:"contracts"`
		} `json:"deployments"`
	} `json:"oracles"`
}
type SupportedContracts struct {
	Input  []Contract `json:"inputSettler"`
	Output []Contract `json:"outputSettler"`
}
type Client struct{ http *transport.Client }

func New(base, key string, rps int) (*Client, error) {
	h := http.Header{}
	if key != "" {
		h.Set("X-API-Key", key)
	}
	c, e := transport.New(base, h, rps)
	if e != nil {
		return nil, e
	}
	return &Client{http: c}, nil
}
func (c *Client) Orders(ctx context.Context, filter url.Values, offset int) (Page, error) {
	if offset < 0 || offset > 1000 {
		return Page{}, errors.New("order pagination exceeds API window")
	}
	q := url.Values{}
	for k, v := range filter {
		q[k] = append([]string(nil), v...)
	}
	q.Set("limit", "50")
	q.Set("offset", strconv.Itoa(offset))
	var p Page
	e := c.http.Do(ctx, http.MethodGet, "/orders?"+q.Encode(), nil, &p)
	return p, e
}
func (c *Client) Catalog(ctx context.Context) (Catalog, error) {
	var v Catalog
	e := c.http.Do(ctx, http.MethodGet, "/api/v1/contracts", nil, &v)
	return v, e
}
func (c *Client) Publish(ctx context.Context, q Quote) error {
	if q.Ranges == nil {
		q.Ranges = []Range{}
	}
	var out struct {
		Status string `json:"status"`
		Added  int    `json:"quotesAdded"`
	}
	if e := c.http.Do(ctx, http.MethodPost, "/quotes/submit", struct {
		Quotes []Quote `json:"quotes"`
	}{[]Quote{q}}, &out); e != nil {
		return e
	}
	if len(q.Ranges) > 0 && out.Added != len(q.Ranges) {
		return errors.New("quote submission not fully accepted")
	}
	return nil
}
func (c *Client) Withdraw(ctx context.Context, q Quote) error {
	q.Ranges = []Range{}
	return c.Publish(ctx, q)
}
func (c *Client) SupportedContracts(ctx context.Context) (SupportedContracts, error) {
	var out struct {
		Data SupportedContracts `json:"data"`
	}
	e := c.http.Do(ctx, http.MethodGet, "/api/v1/solver/supported-contracts", nil, &out)
	return out.Data, e
}
func (c *Client) SetSupportedContracts(ctx context.Context, v SupportedContracts) error {
	return c.http.Do(ctx, http.MethodPut, "/api/v1/solver/supported-contracts", v, nil)
}
func (c *Client) RegistrationMessage(ctx context.Context) (string, error) {
	var out struct {
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	}
	e := c.http.Do(ctx, http.MethodGet, "/api/v1/solver/register/message", nil, &out)
	if e == nil && out.Data.Message == "" {
		e = errors.New("empty registration challenge")
	}
	return out.Data.Message, e
}
func (c *Client) Register(ctx context.Context, message, signature, account, chain string) error {
	return c.http.Do(ctx, http.MethodPost, "/api/v1/solver/register", struct {
		Message   string `json:"message"`
		Signature string `json:"signature"`
		Account   string `json:"account"`
		Chain     string `json:"chain"`
	}{message, signature, account, chain}, nil)
}
func (c *Client) Identities(ctx context.Context) ([]string, error) {
	var out struct {
		Data []struct {
			Address string `json:"address"`
		} `json:"data"`
	}
	if e := c.http.Do(ctx, http.MethodGet, "/solver-api/solver/identities", nil, &out); e != nil {
		return nil, e
	}
	v := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		v = append(v, d.Address)
	}
	return v, nil
}
