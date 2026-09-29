package config

import (
	"testing"

	"github.com/LuisUrrutia/goif-solver/internal/settlement"

	"github.com/ethereum/go-ethereum/common"
)

func TestSingleOrderAuthorizationDoesNotPermitAnotherOrder(t *testing.T) {
	c, err := Load("../../config/sepolia.json")
	if err != nil {
		t.Fatal(err)
	}
	authorized := common.HexToHash("0x01")
	c.IntentAllowlist = []common.Hash{authorized}
	if !c.AllowsIntent(authorized) || c.AllowsIntent(common.HexToHash("0x02")) {
		t.Fatal("single-order scope not enforced")
	}
}

func TestRouteRequiresExplicitSettlementBinding(t *testing.T) {
	for _, id := range []settlement.ID{"", "unknown"} {
		c, err := Load("../../config/sepolia.json")
		if err != nil {
			t.Fatal(err)
		}
		c.Routes[0].Settlement = id
		if err = c.Validate(); err == nil {
			t.Fatal("accepted missing settlement binding", id)
		}
	}
}

func TestSelectedSettlementRequiresItsTypedSettings(t *testing.T) {
	c, err := Load("../../config/sepolia.json")
	if err != nil {
		t.Fatal(err)
	}
	c.Settlement.Backends[c.Routes[0].Settlement] = SettlementBackend{Kind: PolymerSettlement}
	if err = c.Validate(); err == nil {
		t.Fatal("accepted missing provider settings")
	}
}
