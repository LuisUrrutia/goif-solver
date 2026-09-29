package escrow

import (
	"encoding/json"
	"errors"

	"github.com/LuisUrrutia/goif-solver/internal/evm"

	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

type Fulfillment struct {
	Intent IntentData `json:"intent"`
	Fill   FillEvent  `json:"fill"`
}

func SettlementEvidence(v Validated, fill FillEvent) (settlement.Evidence, error) {
	payload, err := json.Marshal(Fulfillment{Intent: Canonical(v), Fill: fill})
	return settlement.Evidence{Kind: IntentKind, Payload: payload}, err
}

func DecodeFulfillment(evidence settlement.Evidence, route Route, signer common.Address) (Validated, FillEvent, error) {
	var data Fulfillment
	if evidence.Kind != IntentKind || json.Unmarshal(evidence.Payload, &data) != nil {
		return Validated{}, FillEvent{}, errors.New("invalid EVM fulfillment evidence")
	}
	order, err := Parse(data.Intent.Order)
	if err != nil {
		return Validated{}, FillEvent{}, err
	}
	id, err := evm.Word(data.Intent.ID)
	if err != nil {
		return Validated{}, FillEvent{}, err
	}
	output := order.Outputs[0]
	if order.OriginChainId.Uint64() != route.OriginChain || output.ChainId.Uint64() != route.DestinationChain || data.Intent.InputSettler != route.InputSettler.Hex() || order.InputOracle != route.InputOracle || output.Oracle != evm.AddressWord(route.OutputOracle) || output.Settler != evm.AddressWord(route.OutputSettler) || common.BigToAddress(order.Inputs[0][0]) != route.InputToken || output.Token != evm.AddressWord(route.OutputToken) {
		return Validated{}, FillEvent{}, errors.New("fulfillment differs from bound route")
	}
	v := Validated{ID: common.Hash(id), Order: order, Route: route}
	fill, err := DecodeFill(&types.Receipt{Logs: []*types.Log{&data.Fill.Log}}, v, signer)
	if err != nil {
		return Validated{}, FillEvent{}, err
	}
	if fill.Solver != data.Fill.Solver || fill.Timestamp != data.Fill.Timestamp {
		return Validated{}, FillEvent{}, errors.New("fill evidence differs from event")
	}
	return v, fill, nil
}
