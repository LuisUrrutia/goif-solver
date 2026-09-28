package escrow

import (
	"github.com/LuisUrrutia/goif-solver/internal/config"
	"testing"
)

func TestQuoteUsesConfiguredAssetsDecimalsAndReserve(t *testing.T) {
	c, err := config.Load("../../config/sepolia.json")
	if err != nil {
		t.Fatal(err)
	}
	route := c.Routes[0]
	route.InputDecimals, route.OutputDecimals = 18, 18
	route.MaxInput, route.MaxOutput, route.MinMargin = "1000000000000000000", "1000000000000000000", "10000000000000000"
	offer, err := Quote(c, route, false)
	if err != nil {
		t.Fatal(err)
	}
	if offer.Input.Decimals != 18 || offer.Output.Decimals != 18 || offer.Input.Address != route.InputToken.Hex() || offer.Output.Address != route.OutputToken.Hex() {
		t.Fatal("configured asset policy lost")
	}
	if len(offer.Ranges) != 1 || offer.Ranges[0].Rate != "0.990000000000000000" || offer.Ranges[0].Minimum != route.MaxInput {
		t.Fatalf("reserve changed: %+v", offer.Ranges)
	}
	withdrawn, err := Quote(c, route, true)
	if err != nil || len(withdrawn.Ranges) != 0 || withdrawn.Input != offer.Input || withdrawn.Output != offer.Output {
		t.Fatal("withdrawal changed route identity")
	}
}
