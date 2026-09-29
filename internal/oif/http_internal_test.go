package oif

import (
	"context"
	"errors"
	"testing"
	"time"
)

type boundedRoute struct {
	Route
	quote func(context.Context) (Quote, error)
}

func (r boundedRoute) Quote(ctx context.Context, _ QuoteRequest) (Quote, error) { return r.quote(ctx) }

func TestQuoteCollectionKeepsHealthyRouteWhenAnotherBlocks(t *testing.T) {
	handler := &Handler{Routes: []Route{
		boundedRoute{quote: func(context.Context) (Quote, error) { return Quote{Provider: "healthy"}, nil }},
		boundedRoute{quote: func(ctx context.Context) (Quote, error) { <-ctx.Done(); return Quote{}, ctx.Err() }},
	}}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	failures, quotes := handler.collectQuotes(ctx, QuoteRequest{})
	if failures[0] != nil || quotes[0].Provider != "healthy" || !errors.Is(failures[1], context.DeadlineExceeded) {
		t.Fatal(failures, quotes)
	}
}

func TestQuoteTokenExpiryIsAuthenticated(t *testing.T) {
	h := &Handler{QuoteKey: []byte("synthetic-test-key-at-least-32-characters")}
	q := Quote{Order: Order{Type: UserOpen}, ValidUntil: time.Now().Add(time.Minute).Unix()}
	token, err := h.sign(q)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.verify(Submission{Order: q.Order, QuoteID: token}); err != nil {
		t.Fatal(err)
	}
	q.ValidUntil = time.Now().Add(-time.Second).Unix()
	token, err = h.sign(q)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.verify(Submission{Order: q.Order, QuoteID: token}); err == nil {
		t.Fatal("accepted expired authenticated quote")
	}
}
