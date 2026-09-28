package evm

import "github.com/LuisUrrutia/goif-solver/internal/intent"

const IntentKind intent.Kind = "evm-escrow"

type OrderData struct {
	User          string       `json:"user"`
	Nonce         string       `json:"nonce"`
	OriginChainID string       `json:"originChainId"`
	FillDeadline  string       `json:"fillDeadline"`
	Expires       string       `json:"expires"`
	InputOracle   string       `json:"inputOracle"`
	Inputs        [][]string   `json:"inputs"`
	Outputs       []OutputData `json:"outputs"`
}
type OutputData struct {
	Oracle       string `json:"oracle"`
	Settler      string `json:"settler"`
	Token        string `json:"token"`
	Amount       string `json:"amount"`
	Recipient    string `json:"recipient"`
	ChainID      string `json:"chainId"`
	CallbackData string `json:"callbackData"`
	Context      string `json:"context"`
}

type IntentData struct {
	ID           string    `json:"id"`
	InputSettler string    `json:"input_settler"`
	Order        OrderData `json:"order"`
}
