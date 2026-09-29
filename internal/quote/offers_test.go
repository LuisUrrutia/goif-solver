package quote

import (
	"testing"
	"time"
)

func TestFixedReserveAcrossVMFamilies(t *testing.T) {
	route := Route{Input: Asset{Chain: "solana:devnet", Address: "USDC-mint", Decimals: 6}, Output: Asset{Chain: "tron:testnet", Address: "USDC-contract", Decimals: 6}, MaxInput: "1000000", MaxOutput: "1000000", Pricing: PricingSettings{Kind: ReservePricing, MinMargin: "10000"}, Solver: "solver-identity", InputValidator: "input-validator", OutputValidator: "output-validator"}
	now := time.Unix(1700000000, 0)
	offer, err := BuildOffer(route, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if offer.Input != route.Input || offer.Output != route.Output || offer.Ranges[0].Rate != "0.99" || offer.Expiry != now.Add(time.Minute).Unix() {
		t.Fatalf("unexpected quote: %+v", offer)
	}
	withdrawn, err := BuildOffer(route, true, now)
	if err != nil || len(withdrawn.Ranges) != 0 || withdrawn.Input != offer.Input || withdrawn.Solver != offer.Solver {
		t.Fatal("withdrawal changed identity", err)
	}
	route.Pricing.MinMargin = route.MaxInput
	if _, err = BuildOffer(route, false, now); err == nil {
		t.Fatal("accepted nonpositive output")
	}
	route.MaxInput = "-1"
	if _, err = BuildOffer(route, false, now); err == nil {
		t.Fatal("accepted negative amount")
	}
}
