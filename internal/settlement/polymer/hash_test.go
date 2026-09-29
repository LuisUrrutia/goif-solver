package polymer

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/ethereum/go-ethereum/common"
)

func pilot(t *testing.T) (evm.IntentData, evm.Route, common.Address) {
	t.Helper()
	b, e := os.ReadFile("../../lifi/testdata/pilot-order.json")
	if e != nil {
		t.Fatal(e)
	}
	var w evm.IntentData
	if e = json.Unmarshal(b, &w); e != nil {
		t.Fatal(e)
	}
	var metadata struct {
		Meta struct {
			ID string `json:"onChainOrderId"`
		} `json:"meta"`
		InputSettler string `json:"inputSettler"`
	}
	if err := json.Unmarshal(b, &metadata); err != nil {
		t.Fatal(err)
	}
	w.ID = metadata.Meta.ID
	w.InputSettler = metadata.InputSettler
	r := evm.Route{Name: "sepolia-base-usdc", OriginChain: 11155111, DestinationChain: 84532, InputSettler: common.HexToAddress(w.InputSettler), OutputSettler: common.HexToAddress("0x75220b7600c300005038432a0000f308e0000068"), InputOracle: common.HexToAddress(w.Order.InputOracle), OutputOracle: common.HexToAddress(w.Order.InputOracle), InputToken: common.HexToAddress("0x1c7d4b196cb0c7b01d743fbc6116a902379c7238"), OutputToken: common.HexToAddress("0x036cbd53842c5426634e7929541ec2318f3dcf7e"), MaxInput: "1000000", MaxOutput: "1000000", MinMargin: "10000", DeadlineBuffer: 30}
	return w, r, common.HexToAddress("0x1fb2bd023d6957e8d01a853fa687db21d08ea045")
}

func TestPilotProofHashIncludesDeployedDomain(t *testing.T) {
	w, _, s := pilot(t)
	o, e := evm.Parse(w.Order)
	if e != nil {
		t.Fatal(e)
	}
	got, err := PayloadHash(common.HexToHash(w.ID), evm.AddressWord(s), 1790619040, o.Outputs[0])
	if err != nil {
		t.Fatal(err)
	}
	if got.Hex() != "0x55253189e1a56e006fcbc7c0a7033109f5e332f2ba92c4914a0d99fb2b575a4c" {
		t.Fatal(got)
	}
}

func TestProofPayloadRejectsTruncatedLengths(t *testing.T) {
	w, _, signer := pilot(t)
	order, err := evm.Parse(w.Order)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"callback", "context"} {
		t.Run(field, func(t *testing.T) {
			out := order.Outputs[0]
			if field == "callback" {
				out.CallbackData = make([]byte, 1<<16)
			} else {
				out.Context = make([]byte, 1<<16)
			}
			if _, err := PayloadHash(common.HexToHash(w.ID), evm.AddressWord(signer), 1790619040, out); err == nil {
				t.Fatal("oversized field length silently truncated")
			}
		})
	}
}
