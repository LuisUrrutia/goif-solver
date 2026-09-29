package evm

import (
	"encoding/json"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

func pilot(t *testing.T) (IntentData, Route, common.Address) {
	t.Helper()
	b, e := os.ReadFile("../lifi/testdata/pilot-order.json")
	if e != nil {
		t.Fatal(e)
	}
	var w IntentData
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
	r := Route{Name: "sepolia-base-usdc", OriginChain: 11155111, DestinationChain: 84532, InputSettler: common.HexToAddress(w.InputSettler), OutputSettler: common.HexToAddress("0x75220b7600c300005038432a0000f308e0000068"), InputOracle: common.HexToAddress(w.Order.InputOracle), OutputOracle: common.HexToAddress(w.Order.InputOracle), InputToken: common.HexToAddress("0x1c7d4b196cb0c7b01d743fbc6116a902379c7238"), OutputToken: common.HexToAddress("0x036cbd53842c5426634e7929541ec2318f3dcf7e"), MaxInput: "1000000", MaxOutput: "1000000", MinMargin: "10000", DeadlineBuffer: 30}
	return w, r, common.HexToAddress("0x1fb2bd023d6957e8d01a853fa687db21d08ea045")
}

func TestPilotOrderMatchesDeployedFillABI(t *testing.T) {
	w, r, s := pilot(t)
	v, e := Validate(w, r, s, time.Unix(1790619000, 0))
	if e != nil {
		t.Fatal(e)
	}
	b, e := OutputABI.Pack("fillOrderOutputs", v.ID, v.Order.Outputs, new(big.Int).SetUint64(uint64(v.Order.FillDeadline)), AddressWord(s))
	_ = b
	if e == nil {
		t.Fatal("ABI must reject bytes32 where dynamic bytes required")
	}
	solver := AddressWord(s)
	b, e = OutputABI.Pack("fillOrderOutputs", v.ID, v.Order.Outputs, new(big.Int).SetUint64(uint64(v.Order.FillDeadline)), solver[:])
	if e != nil {
		t.Fatal(e)
	}
	if common.Bytes2Hex(b[:4]) != "7e7fc653" {
		t.Fatalf("selector %x", b[:4])
	}
	if _, e = InputABI.Pack("orderIdentifier", v.Order); e != nil {
		t.Fatal(e)
	}
}

func TestRejectUnsupportedOrUnsafeOrders(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*IntentData)
	}{
		{"callback", func(w *IntentData) { w.Order.Outputs[0].CallbackData = "0x01" }},
		{"unknown context", func(w *IntentData) { w.Order.Outputs[0].Context = "0x01" }},
		{"amount", func(w *IntentData) { w.Order.Outputs[0].Amount = "1000001" }},
		{"chain", func(w *IntentData) { w.Order.OriginChainID = "1" }},
		{"expired", func(w *IntentData) { w.Order.FillDeadline = "1790619000" }},
		{"numeric overflow", func(w *IntentData) { w.Order.Expires = "4294967296" }},
		{"malformed input", func(w *IntentData) { w.Order.Inputs = [][]string{{"1"}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, r, s := pilot(t)
			tc.change(&w)
			if _, e := Validate(w, r, s, time.Unix(1790619000, 0)); e == nil {
				t.Fatal("accepted unsafe order")
			}
		})
	}
}

func TestDecodeRealPilotFill(t *testing.T) {
	w, r, signer := pilot(t)
	o, e := Parse(w.Order)
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile("testdata/pilot-fill.json")
	if e != nil {
		t.Fatal(e)
	}
	var fixture struct {
		Log types.Log `json:"log"`
	}
	if e = json.Unmarshal(b, &fixture); e != nil {
		t.Fatal(e)
	}
	v := Validated{ID: common.HexToHash(w.ID), Order: o, Route: r}
	fill, e := DecodeFill(&types.Receipt{Logs: []*types.Log{&fixture.Log}}, v, signer)
	if e != nil {
		t.Fatal(e)
	}
	if fill.Timestamp != 1790619040 || fill.Log.Index != 2 {
		t.Fatalf("wrong proof coordinates: %+v", fill)
	}
	v.Order.Outputs[0].Amount = big.NewInt(1)
	if _, e = DecodeFill(&types.Receipt{Logs: []*types.Log{&fixture.Log}}, v, signer); e == nil {
		t.Fatal("accepted mismatched mandate")
	}
}
