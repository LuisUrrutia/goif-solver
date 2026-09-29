package quote

import (
	"math/big"
	"testing"
	"time"
)

func TestRatePricingSharesPrecisionAndCapsWithAdmission(t *testing.T) {
	route := Route{Input: Asset{Decimals: 6}, Output: Asset{Decimals: 18}, MaxInput: "1000000", MaxOutput: "2000000000000000000", Pricing: PricingSettings{Kind: RatePricing, Rate: "1.25"}}
	policy, err := NewPricing(route.Pricing, route.MaxInput, route.MaxOutput, 6, 18)
	if err != nil {
		t.Fatal(err)
	}
	offer, err := BuildOffer(route, false, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	if offer.Ranges[0].Rate != "1.25" {
		t.Fatal(offer.Ranges)
	}
	output, err := policy.Output(big.NewInt(1000000))
	if err != nil || output.String() != "1250000000000000000" {
		t.Fatal(output, err)
	}
	if err = Admit(policy, big.NewInt(1000000), output); err != nil {
		t.Fatal(err)
	}
	if err = Admit(policy, big.NewInt(1000000), new(big.Int).Add(output, big.NewInt(1))); err == nil {
		t.Fatal("admission exceeded quote")
	}
	if _, err = policy.Output(big.NewInt(1000001)); err == nil {
		t.Fatal("input cap bypassed")
	}
	capped, err := NewPricing(route.Pricing, route.MaxInput, "1200000000000000000", 6, 18)
	if err != nil {
		t.Fatal(err)
	}
	value, err := capped.Output(big.NewInt(1000000))
	if err != nil || value.String() != "1200000000000000000" {
		t.Fatal(value, err)
	}
}

func TestPricingRejectsAmbiguousOrUnsafeStrategies(t *testing.T) {
	for _, settings := range []PricingSettings{{Kind: ReservePricing, MinMargin: "1"}, {Kind: RatePricing, Rate: "0"}, {Kind: RatePricing, Rate: "1/2"}, {Kind: RatePricing, Rate: "1", MinMargin: "1"}, {Kind: "unknown"}} {
		if _, err := NewPricing(settings, "1000000", "1000000000000000000", 6, 18); err == nil {
			t.Fatal("accepted", settings)
		}
	}
}
