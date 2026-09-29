package quote

import (
	"errors"
	"math/big"
	"time"
)

type Route struct {
	Input, Output                           Asset
	MaxInput, MaxOutput, MinMargin          string
	Solver, InputValidator, OutputValidator string
}

func FixedReserve(route Route, withdraw bool, now time.Time) (Offer, error) {
	if route.Input.Decimals != route.Output.Decimals {
		return Offer{}, errors.New("fixed reserve requires equal asset decimals")
	}

	input, err := amount(route.MaxInput)
	if err != nil {
		return Offer{}, err
	}
	margin, err := amount(route.MinMargin)
	if err != nil {
		return Offer{}, err
	}
	cap, err := amount(route.MaxOutput)
	if err != nil {
		return Offer{}, err
	}
	output := new(big.Int).Sub(input, margin)
	if output.Cmp(cap) > 0 {
		output = cap
	}
	if output.Sign() <= 0 {
		return Offer{}, errors.New("route cost reserve exceeds input")
	}
	offer := Offer{Input: route.Input, Output: route.Output, Expiry: now.Add(time.Minute).Unix(), Solver: route.Solver, InputValidator: route.InputValidator, OutputValidator: route.OutputValidator}

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
		offer.Ranges = []PriceRange{{Minimum: input.String(), Maximum: input.String(), Rate: rate}}
	}
	return offer, nil
}

func amount(value string) (*big.Int, error) {
	if value == "" {
		return nil, errors.New("empty amount")
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
