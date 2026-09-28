package escrow

import (
	"errors"
	"math/big"
	"strconv"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/quote"
)

func Quote(c config.Config, route evm.Route, withdraw bool) (quote.Offer, error) {
	input, err := evm.Uint(route.MaxInput, 256)
	if err != nil {
		return quote.Offer{}, err
	}
	margin, err := evm.Uint(route.MinMargin, 256)
	if err != nil {
		return quote.Offer{}, err
	}
	cap, err := evm.Uint(route.MaxOutput, 256)
	if err != nil {
		return quote.Offer{}, err
	}
	output := new(big.Int).Sub(input, margin)
	if output.Cmp(cap) > 0 {
		output = cap
	}
	if output.Sign() <= 0 {
		return quote.Offer{}, errors.New("route cost reserve exceeds input")
	}
	var exclusive string
	for _, signer := range c.Signers {
		if signer.Name == route.Signer {
			exclusive = signer.Address.Hex()
		}
	}
	offer := quote.Offer{Input: quote.Asset{Chain: "eip155:" + strconv.FormatUint(route.OriginChain, 10), Address: route.InputToken.Hex(), Decimals: route.InputDecimals}, Output: quote.Asset{Chain: "eip155:" + strconv.FormatUint(route.DestinationChain, 10), Address: route.OutputToken.Hex(), Decimals: route.OutputDecimals}, Expiry: time.Now().Add(time.Minute).Unix(), Solver: exclusive, InputValidator: route.InputOracle.Hex(), OutputValidator: route.OutputOracle.Hex()}

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
		offer.Ranges = []quote.PriceRange{{Minimum: input.String(), Maximum: input.String(), Rate: rate}}
	}
	return offer, nil
}
