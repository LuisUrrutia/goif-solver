package escrow

import (
	"context"
	"errors"
	"math/big"

	"github.com/LuisUrrutia/goif-solver/internal/evm"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

func ZeroGovernanceFee(ctx context.Context, c *ethclient.Client, settler common.Address) error {
	for _, method := range []string{"governanceFee", "nextGovernanceFee"} {
		values, e := evm.Call(ctx, c, settler, InputABI, nil, method)
		if e != nil {
			return e
		}
		switch n := values[0].(type) {
		case uint64:
			if n != 0 {
				return errors.New("nonzero current or pending governance fee unsupported")
			}
		case *big.Int:
			if n.Sign() != 0 {
				return errors.New("nonzero governance fee unsupported")
			}
		default:
			return errors.New("invalid governance fee response")
		}
	}
	return nil
}
