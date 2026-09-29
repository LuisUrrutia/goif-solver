package app

import (
	"strconv"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/quote"
)

func EVMQuote(c config.Config, route evm.Route, withdraw bool) (quote.Offer, error) {
	var signer string
	for _, s := range c.Signers {
		if s.Name == route.Signer {
			signer = s.Address.Hex()
		}
	}
	return quote.FixedReserve(quote.Route{Input: quote.Asset{Chain: "eip155:" + strconv.FormatUint(route.OriginChain, 10), Address: route.InputToken.Hex(), Decimals: route.InputDecimals}, Output: quote.Asset{Chain: "eip155:" + strconv.FormatUint(route.DestinationChain, 10), Address: route.OutputToken.Hex(), Decimals: route.OutputDecimals}, MaxInput: route.MaxInput, MaxOutput: route.MaxOutput, MinMargin: route.MinMargin, Solver: signer, InputValidator: route.InputOracle.Hex(), OutputValidator: route.OutputOracle.Hex()}, withdraw, time.Now())
}
