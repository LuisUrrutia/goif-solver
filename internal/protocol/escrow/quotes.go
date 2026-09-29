package escrow

import (
	"context"
	"math/big"
	"strconv"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/evm"

	"github.com/LuisUrrutia/goif-solver/internal/quote"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

type QuoteSource struct {
	client   *ethclient.Client
	required *big.Int
	route    quote.Route
	signer   common.Address
	token    common.Address
}

func NewQuoteSource(route Route, signer common.Address, client *ethclient.Client) (*QuoteSource, error) {
	input, err := evm.Uint(route.MaxInput, 256)
	if err != nil {
		return nil, err
	}
	pricing, err := quote.NewPricing(route.Pricing, route.MaxInput, route.MaxOutput, route.InputDecimals, route.OutputDecimals)
	if err != nil {
		return nil, err
	}
	required, err := pricing.Output(input)
	if err != nil {
		return nil, err
	}
	policy := quote.Route{
		Input:    quote.Asset{Chain: "eip155:" + strconv.FormatUint(route.OriginChain, 10), Address: route.InputToken.Hex(), Decimals: route.InputDecimals},
		Output:   quote.Asset{Chain: "eip155:" + strconv.FormatUint(route.DestinationChain, 10), Address: route.OutputToken.Hex(), Decimals: route.OutputDecimals},
		MaxInput: route.MaxInput, MaxOutput: route.MaxOutput, Pricing: route.Pricing,
		Solver: signer.Hex(), InputValidator: route.InputOracle.Hex(), OutputValidator: route.OutputOracle.Hex(),
	}
	return &QuoteSource{client: client, required: required, route: policy, signer: signer, token: route.OutputToken}, nil
}

func (s *QuoteSource) Offer(ctx context.Context, withdraw bool) (quote.Offer, error) {
	if !withdraw {
		covered, err := s.Covers(ctx, s.required)
		if err != nil {
			return quote.Offer{}, err
		}
		withdraw = !covered
	}
	return quote.BuildOffer(s.route, withdraw, time.Now())
}

func (s *QuoteSource) Covers(ctx context.Context, output *big.Int) (bool, error) {
	balance, err := evm.Balance(ctx, s.client, s.token, s.signer)
	if err != nil {
		return false, err
	}
	return balance.Cmp(output) >= 0, nil
}
