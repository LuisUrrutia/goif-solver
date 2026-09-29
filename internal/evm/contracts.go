package evm

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/LuisUrrutia/goif-solver/internal/transport"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

//go:embed abi/*.json
var abiFiles embed.FS

var TokenABI = loadABI("erc20")

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

func Call(ctx context.Context, c *ethclient.Client, address common.Address, a abi.ABI, block *big.Int, method string, args ...interface{}) ([]interface{}, error) {
	data, e := a.Pack(method, args...)
	if e != nil {
		return nil, fmt.Errorf("encode %s: %w", method, e)
	}
	b, e := c.CallContract(ctx, ethereum.CallMsg{To: &address, Data: data}, block)
	if e != nil {
		return nil, transport.Failure(ctx, "contract call "+method, e)
	}
	out, e := a.Unpack(method, b)
	if e != nil {
		return nil, fmt.Errorf("decode %s", method)
	}
	return out, nil
}

func Balance(ctx context.Context, c *ethclient.Client, token, owner common.Address) (*big.Int, error) {
	out, e := Call(ctx, c, token, TokenABI, nil, "balanceOf", owner)
	if e != nil {
		return nil, e
	}
	n, ok := out[0].(*big.Int)
	if !ok {
		return nil, errors.New("invalid token balance")
	}
	return n, nil
}
