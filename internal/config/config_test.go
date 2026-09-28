package config

import (
	"testing"

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
