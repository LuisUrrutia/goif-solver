package evm

import (
	"context"
	"math/big"
	"strconv"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/quote"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

type QuoteSource struct {
	client *ethclient.Client
	cap    *big.Int
	route  quote.Route
	signer common.Address
	token  common.Address
}

func NewQuoteSource(route Route, signer common.Address, client *ethclient.Client) (*QuoteSource, error) {
	cap, err := Uint(route.MaxOutput, 256)
	if err != nil {
		return nil, err
	}
	policy := quote.Route{
		Input:    quote.Asset{Chain: "eip155:" + strconv.FormatUint(route.OriginChain, 10), Address: route.InputToken.Hex(), Decimals: route.InputDecimals},
		Output:   quote.Asset{Chain: "eip155:" + strconv.FormatUint(route.DestinationChain, 10), Address: route.OutputToken.Hex(), Decimals: route.OutputDecimals},
		MaxInput: route.MaxInput, MaxOutput: route.MaxOutput, MinMargin: route.MinMargin,
		Solver: signer.Hex(), InputValidator: route.InputOracle.Hex(), OutputValidator: route.OutputOracle.Hex(),
	}
	return &QuoteSource{client: client, cap: cap, route: policy, signer: signer, token: route.OutputToken}, nil
}

func (s *QuoteSource) Offer(ctx context.Context, withdraw bool) (quote.Offer, error) {
	if !withdraw {
		balance, err := Balance(ctx, s.client, s.token, s.signer)
		if err != nil {
			return quote.Offer{}, err
		}
		withdraw = balance.Cmp(s.cap) < 0
	}
	return quote.FixedReserve(s.route, withdraw, time.Now())
}
