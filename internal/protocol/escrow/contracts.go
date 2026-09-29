package escrow

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"math/big"
	"strings"

	"github.com/LuisUrrutia/goif-solver/internal/evm"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
)

//go:embed abi/*.json
var abiFiles embed.FS

const (
	InputSettlerRuntime  = "input-settler"
	OutputSettlerRuntime = "output-settler"
)

var (
	InputABI  = loadABI(InputSettlerRuntime)
	OutputABI = loadABI(OutputSettlerRuntime)
)

func loadABI(name string) abi.ABI {
	b, e := abiFiles.ReadFile("abi/" + name + ".json")
	if e != nil {
		panic(e)
	}
	a, e := abi.JSON(strings.NewReader(string(b)))
	if e != nil {
		panic(e)
	}
	return a
}

type EscrowStatus uint8

const (
	EscrowNone EscrowStatus = iota
	EscrowDeposited
	EscrowClaimed
	EscrowRefunded
)

func OrderStatus(ctx context.Context, c *ethclient.Client, v Validated, block *big.Int) (EscrowStatus, error) {
	out, e := evm.Call(ctx, c, v.Route.InputSettler, InputABI, block, "orderIdentifier", v.Order)
	if e != nil {
		return 0, e
	}
	id, ok := out[0].([32]byte)
	if !ok || common.Hash(id) != v.ID {
		return 0, errors.New("on-chain order identifier mismatch")
	}
	out, e = evm.Call(ctx, c, v.Route.InputSettler, InputABI, block, "orderStatus", v.ID)
	if e != nil {
		return 0, e
	}
	status, ok := out[0].(uint8)
	if !ok {
		return 0, errors.New("invalid order status")
	}
	if status > uint8(EscrowRefunded) {
		return EscrowNone, errors.New("unknown escrow status")
	}
	return EscrowStatus(status), nil
}

type FillEvent struct {
	Log       types.Log
	Solver    [32]byte
	Timestamp uint32
}

func DecodeFill(receipt *types.Receipt, v Validated, solver common.Address) (FillEvent, error) {
	event := OutputABI.Events["OutputFilled"]
	for _, log := range receipt.Logs {
		if log.Address != v.Route.OutputSettler || len(log.Topics) != 2 || log.Topics[0] != event.ID || log.Topics[1] != v.ID || log.Removed {
			continue
		}
		values, err := event.Inputs.NonIndexed().Unpack(log.Data)
		if err != nil || len(values) != 4 {
			return FillEvent{}, errors.New("invalid fill event")
		}
		s, ok := values[0].([32]byte)
		if !ok || s != evm.AddressWord(solver) {
			return FillEvent{}, errors.New("unexpected fill solver")
		}
		ts, ok := values[1].(uint32)
		if !ok {
			return FillEvent{}, errors.New("invalid fill timestamp")
		}
		actual, err := event.Inputs.NonIndexed().Pack(values...)
		if err != nil {
			return FillEvent{}, err
		}
		expected, err := event.Inputs.NonIndexed().Pack(s, ts, v.Order.Outputs[0], v.Order.Outputs[0].Amount)
		if err != nil || string(actual) != string(expected) {
			return FillEvent{}, errors.New("fill mandate or final amount mismatch")
		}
		return FillEvent{Solver: s, Timestamp: ts, Log: *log}, nil
	}
	return FillEvent{}, errors.New("matching fill event not found")
}

func VerifyRuntime(name string, code []byte) error {
	b, err := abiFiles.ReadFile("abi/provenance.json")
	if err != nil {
		return err
	}
	var provenance struct {
		Contracts []struct {
			File string      `json:"file"`
			Hash common.Hash `json:"runtime_keccak256"`
		} `json:"contracts"`
	}
	if err := json.Unmarshal(b, &provenance); err != nil {
		return err
	}
	for _, contract := range provenance.Contracts {
		if contract.File == name+".json" {
			if crypto.Keccak256Hash(code) != contract.Hash {
				return errors.New("contract runtime differs from verified deployment")
			}
			return nil
		}
	}
	return errors.New("contract runtime not pinned")
}
