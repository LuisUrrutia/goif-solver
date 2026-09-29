// Package lifi implements the LI.FI development order-server contract.
package lifi

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"strconv"

	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"

	"github.com/LuisUrrutia/goif-solver/internal/quote"
	"github.com/LuisUrrutia/goif-solver/internal/transport"
)

type Envelope struct {
	Meta struct {
		ID     string `json:"onChainOrderId"`
		Status string `json:"orderStatus"`
		FillTx string `json:"orderDeliveredTxHash,omitempty"`
	} `json:"meta"`
	InputSettler string                   `json:"inputSettler"`
	Order        escrowprotocol.OrderData `json:"order"`
}
type Page struct {
	Data []json.RawMessage `json:"data"`
	Meta struct {
		Total  int `json:"total"`
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
	} `json:"meta"`
}

const (
	orderPageSize  = 50
	maxOrderOffset = 1000
)

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
	ExclusiveFor string  `json:"exclusiveFor,omitempty"`
	Ranges       []Range `json:"ranges"`
	FromDecimals int     `json:"fromDecimals"`
	ToDecimals   int     `json:"toDecimals"`
	Expiry       int64   `json:"expiry"`
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
	if offset < 0 || offset > maxOrderOffset {
		return Page{}, errors.New("order pagination exceeds API window")
	}
	q := url.Values{}
	for k, v := range filter {
		q[k] = append([]string(nil), v...)
	}
	q.Set("limit", strconv.Itoa(orderPageSize))
	q.Set("offset", strconv.Itoa(offset))
	var p Page
	e := c.http.Do(ctx, http.MethodGet, "/orders?"+q.Encode(), nil, &p)
	if e == nil && (p.Data == nil || len(p.Data) > orderPageSize || p.Meta.Total < 0 || p.Meta.Offset != offset) {
		e = errors.New("invalid order page bounds")
	}
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
	}{Quotes: []Quote{q}}, &out); e != nil {
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
	}{Message: message, Signature: signature, Account: account, Chain: chain}, nil)
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

// VerifyQuote reads the submitted route back; an HTTP success alone does not
// establish that inventory was published or withdrawn.
func (c *Client) VerifyQuote(ctx context.Context, q Quote) error {
	filter := url.Values{"fromChain": {q.FromChain}, "toChain": {q.ToChain}, "fromAsset": {q.FromAsset}, "toAsset": {q.ToAsset}, "limit": {"50"}}
	var out struct {
		Data []struct {
			MinAmount string `json:"minAmount"`
			MaxAmount string `json:"maxAmount"`
			Quote     string `json:"quote"`
		} `json:"data"`
		Meta struct {
			Total int `json:"total"`
		} `json:"meta"`
	}
	if err := c.http.Do(ctx, http.MethodGet, "/solver-api/quotes?"+filter.Encode(), nil, &out); err != nil {
		return err
	}
	if len(out.Data) != len(q.Ranges) || out.Meta.Total != len(q.Ranges) {
		return errors.New("quote readback range count differs")
	}
	for i, want := range q.Ranges {
		got := out.Data[i]
		a, ok := new(big.Rat).SetString(want.Quote)
		if !ok {
			return errors.New("invalid quote rate")
		}
		b, ok := new(big.Rat).SetString(got.Quote)
		if !ok || a.Cmp(b) != 0 || got.MinAmount != want.MinAmount || got.MaxAmount != want.MaxAmount {
			return errors.New("quote readback differs")
		}
	}
	return nil
}

func (c *Client) Order(ctx context.Context, id string) (Envelope, error) {
	var out Envelope
	err := c.http.Do(ctx, http.MethodGet, "/orders/status?"+url.Values{"onChainOrderId": {id}}.Encode(), nil, &out)
	return out, err
}

func (e Envelope) Intent() escrowprotocol.IntentData {
	return escrowprotocol.IntentData{ID: e.Meta.ID, InputSettler: e.InputSettler, Order: e.Order}
}

func (c *Client) PublishOffer(ctx context.Context, offer quote.Offer) error {
	q := Quote{FromChain: offer.Input.Chain, ToChain: offer.Output.Chain, FromAsset: offer.Input.Address, ToAsset: offer.Output.Address, FromDecimals: int(offer.Input.Decimals), ToDecimals: int(offer.Output.Decimals), Expiry: offer.Expiry, ExclusiveFor: offer.Solver, Ranges: []Range{}}
	for _, price := range offer.Ranges {
		q.Ranges = append(q.Ranges, Range{MinAmount: price.Minimum, MaxAmount: price.Maximum, Quote: price.Rate, OracleCosts: []OracleCost{{InputOracle: offer.InputValidator, OutputOracle: offer.OutputValidator, FixedCost: "0"}}})
	}
	if err := c.Publish(ctx, q); err != nil {
		return err
	}
	return c.VerifyQuote(ctx, q)
}
