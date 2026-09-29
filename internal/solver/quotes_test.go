package solver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/quote"
	"github.com/LuisUrrutia/goif-solver/internal/storage/memorystore"
)

type quoteSourceFunc func(context.Context, bool) (quote.Offer, error)

func (f quoteSourceFunc) Offer(ctx context.Context, withdraw bool) (quote.Offer, error) {
	return f(ctx, withdraw)
}

type recordingPublisher struct {
	offers []quote.Offer
	err    error
}

func (p *recordingPublisher) PublishOffer(_ context.Context, offer quote.Offer) error {
	if p.err != nil {
		return p.err
	}
	p.offers = append(p.offers, offer)
	return nil
}

func TestQuoterPublishesAndWithdrawsAcrossVMFamilies(t *testing.T) {
	route := quote.Route{Input: quote.Asset{Chain: "solana:devnet", Address: "mint", Decimals: 6}, Output: quote.Asset{Chain: "tron:nile", Address: "token", Decimals: 6}, Solver: "solver-public-key", MaxInput: "1000000", MaxOutput: "1000000", Pricing: quote.PricingSettings{Kind: quote.ReservePricing, MinMargin: "10000"}}
	publisher := &recordingPublisher{}
	source := quoteSourceFunc(func(_ context.Context, withdraw bool) (quote.Offer, error) {
		return quote.BuildOffer(route, withdraw, time.Now())
	})
	q := Quoter{Store: memorystore.New(), Sources: []quote.Binding{{Name: "svm-tvm", Source: source, Publisher: publisher}}, Enabled: true}

	if err := q.Refresh(t.Context(), false); err != nil {
		t.Fatal(err)
	}
	if err := q.Refresh(t.Context(), true); err != nil {
		t.Fatal(err)
	}

	if len(publisher.offers) != 2 {
		t.Fatal("missing publications")
	}
	active, withdrawn := publisher.offers[0], publisher.offers[1]
	if active.Input != route.Input || active.Output != route.Output || active.Solver != route.Solver || len(active.Ranges) != 1 {
		t.Fatal("lost non-EVM identity or pricing")
	}
	if withdrawn.Input != active.Input || withdrawn.Output != active.Output || withdrawn.Solver != active.Solver || len(withdrawn.Ranges) != 0 {
		t.Fatal("withdrawal changed route identity")
	}
}

func TestQuoterCoordinatesAndReleasesOnPublicationFailure(t *testing.T) {
	store := memorystore.New()
	publisher := &recordingPublisher{err: errors.New("publisher unavailable")}
	calls := 0
	source := quoteSourceFunc(func(context.Context, bool) (quote.Offer, error) {
		calls++
		return quote.Offer{Expiry: time.Now().Add(time.Minute).Unix()}, nil
	})
	q := Quoter{Store: store, Sources: []quote.Binding{{Source: source, Publisher: publisher}}}
	if err := q.Refresh(t.Context(), false); !errors.Is(err, intent.ErrObserve) || calls != 0 {
		t.Fatal("observation caused effects", err)
	}
	q.Enabled = true
	lease, err := store.Acquire(t.Context(), coordination.QuoteLease(""), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = q.Refresh(t.Context(), false); err != nil || calls != 0 {
		t.Fatal("competing publisher was not excluded", err)
	}
	if err = store.Release(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
	if err = q.Refresh(t.Context(), false); !errors.Is(err, publisher.err) {
		t.Fatal("publication failure lost", err)
	}
	lease, err = store.Acquire(t.Context(), coordination.QuoteLease(""), time.Minute)
	if err != nil {
		t.Fatal("failed publication retained lease", err)
	}
	if err = store.Release(t.Context(), lease); err != nil {
		t.Fatal(err)
	}
}

func TestQuoterContinuesAfterIndependentRouteFailure(t *testing.T) {
	failure := errors.New("inventory RPC unavailable")
	publisher := &recordingPublisher{}
	q := Quoter{Store: memorystore.New(), Enabled: true, Sources: []quote.Binding{
		{Name: "unavailable", Publisher: publisher, Source: quoteSourceFunc(func(context.Context, bool) (quote.Offer, error) { return quote.Offer{}, failure })},
		{Name: "healthy", Publisher: publisher, Source: quoteSourceFunc(func(context.Context, bool) (quote.Offer, error) {
			return quote.Offer{Solver: "healthy", Expiry: time.Now().Add(time.Minute).Unix()}, nil
		})},
	}}

	err := q.Refresh(t.Context(), false)

	if !errors.Is(err, failure) || len(publisher.offers) != 1 || publisher.offers[0].Solver != "healthy" {
		t.Fatalf("independent route blocked: %v %+v", err, publisher.offers)
	}
}

func TestRenewalUsesRemainingOfferValidity(t *testing.T) {
	now := time.Now()
	for _, lifetime := range []time.Duration{time.Minute, 10 * time.Second, time.Second} {
		next := renewalAt(now, now.Add(lifetime))
		if !next.After(now) || !next.Before(now.Add(lifetime)) {
			t.Fatalf("renewal outside offer lifetime: %s", lifetime)
		}
	}
}

func TestQuoterRunsHealthyRouteWhileAnotherIsBlocked(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	healthy := make(chan struct{}, 1)
	publisher := &recordingPublisher{}
	q := Quoter{Store: memorystore.New(), Enabled: true, Sources: []quote.Binding{
		{Name: "blocked", Publisher: publisher, Source: quoteSourceFunc(func(ctx context.Context, _ bool) (quote.Offer, error) { <-ctx.Done(); return quote.Offer{}, ctx.Err() })},
		{Name: "healthy", Publisher: publisher, Source: quoteSourceFunc(func(context.Context, bool) (quote.Offer, error) {
			healthy <- struct{}{}
			return quote.Offer{Expiry: time.Now().Add(time.Minute).Unix()}, nil
		})},
	}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		q.Run(ctx, func(context.Context) (bool, error) { return false, nil }, func(error) {})
	}()
	select {
	case <-healthy:
	case <-ctx.Done():
		t.Fatal("blocked route starved healthy route")
	}
	cancel()
	<-done
}
