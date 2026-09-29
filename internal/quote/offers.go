package quote

import (
	"errors"
	"math/big"
	"strings"
	"time"
)

type Route struct {
	Input, Output                           Asset
	MaxInput, MaxOutput                     string
	Pricing                                 PricingSettings
	Solver, InputValidator, OutputValidator string
}

func BuildOffer(route Route, withdraw bool, now time.Time) (Offer, error) {
	policy, err := NewPricing(route.Pricing, route.MaxInput, route.MaxOutput, route.Input.Decimals, route.Output.Decimals)
	if err != nil {
		return Offer{}, err
	}
	input, err := amount(route.MaxInput)
	if err != nil {
		return Offer{}, err
	}
	output, err := policy.Output(input)
	if err != nil {
		return Offer{}, err
	}
	offer := Offer{Input: route.Input, Output: route.Output, Expiry: now.Add(time.Minute).Unix(), Solver: route.Solver, InputValidator: route.InputValidator, OutputValidator: route.OutputValidator}

	if !withdraw {
		// Prices are output asset units per input asset unit, rounded down.
		numerator := new(big.Int).Mul(output, power(route.Input.Decimals))
		denominator := new(big.Int).Mul(input, power(route.Output.Decimals))
		scale := power(36)
		scaled := new(big.Int).Quo(new(big.Int).Mul(numerator, scale), denominator)
		integer, remainder := new(big.Int), new(big.Int)
		integer.QuoRem(scaled, scale, remainder)
		fraction := remainder.String()
		for len(fraction) < 36 {
			fraction = "0" + fraction
		}
		rate := strings.TrimRight(strings.TrimRight(integer.String()+"."+fraction, "0"), ".")
		if scaled.Sign() <= 0 {
			return Offer{}, errors.New("quote rate is below supported precision")
		}

		offer.Ranges = []PriceRange{{Minimum: input.String(), Maximum: input.String(), Rate: rate}}
	}
	return offer, nil
}

func amount(value string) (*big.Int, error) {
	if value == "" || len(value) > 78 || len(value) > 1 && value[0] == '0' {
		return nil, errors.New("noncanonical amount")
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return nil, errors.New("amount must be unsigned decimal")
		}
	}
	n, ok := new(big.Int).SetString(value, 10)
	if !ok || n.BitLen() > 256 {
		return nil, errors.New("amount exceeds 256 bits")
	}
	return n, nil
}
