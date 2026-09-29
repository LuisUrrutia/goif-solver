package lifi

import (
	"bytes"
	"encoding/json"
	"errors"
	"math/big"

	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"
)

// Historical API records mix JSON numbers and strings for Solidity integers.
// Decode their decimal text directly; float64 would round nonces and amounts.
type decimalInteger string

func (n *decimalInteger) UnmarshalJSON(raw []byte) error {
	value := string(raw)
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
	}
	if value == "" || len(value) > 78 || len(value) > 1 && value[0] == '0' {
		return errors.New("invalid unsigned decimal integer")
	}
	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return errors.New("invalid unsigned decimal integer")
		}
	}
	integer, ok := new(big.Int).SetString(value, 10)
	if !ok || integer.BitLen() > 256 {
		return errors.New("unsigned integer overflow")
	}
	*n = decimalInteger(value)
	return nil
}

type wireOutput struct {
	escrowprotocol.OutputData
	Amount  decimalInteger `json:"amount"`
	ChainID decimalInteger `json:"chainId"`
}

type wireOrder struct {
	escrowprotocol.OrderData
	Nonce         decimalInteger     `json:"nonce"`
	OriginChainID decimalInteger     `json:"originChainId"`
	FillDeadline  decimalInteger     `json:"fillDeadline"`
	Expires       decimalInteger     `json:"expires"`
	Inputs        [][]decimalInteger `json:"inputs"`
	Outputs       []wireOutput       `json:"outputs"`
}

func (e *Envelope) UnmarshalJSON(raw []byte) error {
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("intent envelope must be an object")
	}
	type plainEnvelope Envelope
	var result Envelope
	wire := struct {
		*plainEnvelope
		Order wireOrder `json:"order"`
	}{plainEnvelope: (*plainEnvelope)(&result)}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	order := wire.Order.OrderData
	order.Nonce = string(wire.Order.Nonce)
	order.OriginChainID = string(wire.Order.OriginChainID)
	order.FillDeadline = string(wire.Order.FillDeadline)
	order.Expires = string(wire.Order.Expires)
	order.Inputs = make([][]string, len(wire.Order.Inputs))
	for i, input := range wire.Order.Inputs {
		order.Inputs[i] = make([]string, len(input))
		for j, value := range input {
			order.Inputs[i][j] = string(value)
		}
	}
	order.Outputs = make([]escrowprotocol.OutputData, len(wire.Order.Outputs))
	for i, output := range wire.Order.Outputs {
		order.Outputs[i] = output.OutputData
		order.Outputs[i].Amount = string(output.Amount)
		order.Outputs[i].ChainID = string(output.ChainID)
	}
	result.Order = order
	*e = result
	return nil
}
