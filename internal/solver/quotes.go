package solver

import (
	"context"
	"errors"
	"math/big"
	"strconv"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
)

// Quote prices one fixed-size development ticket. MinMargin is a configured
// USDC cost reserve; it is not a native-gas conversion or a market price feed.
func (s *Service) Quote(route evm.Route, withdraw bool) (lifi.Quote, error) {
	input, err := evm.Uint(route.MaxInput, 256)
	if err != nil {
		return lifi.Quote{}, err
	}
	margin, err := evm.Uint(route.MinMargin, 256)
	if err != nil {
		return lifi.Quote{}, err
	}
	cap, err := evm.Uint(route.MaxOutput, 256)
	if err != nil {
		return lifi.Quote{}, err
	}
	output := new(big.Int).Sub(input, margin)
	if output.Cmp(cap) > 0 {
		output = cap
	}
	if output.Sign() <= 0 {
		return lifi.Quote{}, errors.New("route cost reserve exceeds input")
	}
	var exclusive string
	for _, signer := range s.Engine.Config.Signers {
		if signer.Name == route.Signer {
			exclusive = signer.Address.Hex()
		}
	}
	quote := lifi.Quote{FromChain: "eip155:" + strconv.FormatUint(route.OriginChain, 10), ToChain: "eip155:" + strconv.FormatUint(route.DestinationChain, 10), FromAsset: route.InputToken.Hex(), ToAsset: route.OutputToken.Hex(), FromDecimals: 6, ToDecimals: 6, Expiry: time.Now().Add(time.Minute).Unix(), ExclusiveFor: exclusive, Ranges: []lifi.Range{}}
	if !withdraw {
		// Truncate toward zero so rounding cannot advertise more than the reserve.
		scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
		scaled := new(big.Int).Quo(new(big.Int).Mul(output, scale), input)
		integer, remainder := new(big.Int), new(big.Int)
		integer.QuoRem(scaled, scale, remainder)
		fraction := remainder.String()
		for len(fraction) < 18 {
			fraction = "0" + fraction
		}
		rate := integer.String() + "." + fraction
		quote.Ranges = []lifi.Range{{MinAmount: input.String(), MaxAmount: input.String(), Quote: rate, OracleCosts: []lifi.OracleCost{{InputOracle: route.InputOracle.Hex(), OutputOracle: route.OutputOracle.Hex(), FixedCost: "0"}}}}
	}
	return quote, nil
}
func (s *Service) PublishQuotes(ctx context.Context, withdraw bool) error {
	if !s.Engine.Execute {
		return ErrObserve
	}
	lease, err := s.Engine.Store.Acquire(ctx, "quotes", 45*time.Second)
	if errors.Is(err, coordination.ErrBusy) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = s.Engine.Store.Release(context.WithoutCancel(ctx), lease) }()
	for _, route := range s.Engine.Config.Routes {
		disabled := withdraw
		if !disabled {
			sender := s.Engine.Senders[route.Signer][route.DestinationChain]
			balance, err := evm.Balance(ctx, sender.Client, route.OutputToken, sender.Signer.Address())
			if err != nil {
				return err
			}
			cap, _ := evm.Uint(route.MaxOutput, 256)
			disabled = balance.Cmp(cap) < 0
		}
		quote, err := s.Quote(route, disabled)
		if err != nil {
			return err
		}
		if err = s.Engine.Store.Renew(ctx, lease, 45*time.Second); err != nil {
			return err
		}
		request, cancel := context.WithTimeout(ctx, 20*time.Second)
		err = s.API.Publish(request, quote)
		if err == nil {
			err = s.API.VerifyQuote(request, quote)
		}
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}
