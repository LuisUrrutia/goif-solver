package quote

import (
	"errors"
	"math/big"
	"regexp"
)

type PricingKind string

const (
	ReservePricing PricingKind = "fixed-reserve"
	RatePricing    PricingKind = "fixed-rate"
)

type PricingSettings struct {
	Kind      PricingKind `json:"kind"`
	MinMargin string      `json:"min_margin,omitempty"`
	Rate      string      `json:"rate,omitempty"`
}

type Pricing interface {
	Output(*big.Int) (*big.Int, error)
}
type cappedPricing struct {
	input, output *big.Int
	price         func(*big.Int) *big.Int
}

func (p cappedPricing) Output(input *big.Int) (*big.Int, error) {
	if input.Sign() <= 0 || input.Cmp(p.input) > 0 {
		return nil, errors.New("input exceeds pricing limits")
	}
	output := p.price(input)
	if output.Sign() <= 0 {
		return nil, errors.New("pricing yields nonpositive output")
	}
	if output.Cmp(p.output) > 0 {
		output.Set(p.output)
	}
	return output, nil
}

var decimalRate = regexp.MustCompile(`^[0-9]{1,36}(\.[0-9]{1,36})?$`)

func NewPricing(settings PricingSettings, maxInput, maxOutput string, inputDecimals, outputDecimals uint8) (Pricing, error) {
	input, err := amount(maxInput)
	if err != nil || input.Sign() <= 0 {
		return nil, errors.New("invalid pricing input limit")
	}
	output, err := amount(maxOutput)
	if err != nil || output.Sign() <= 0 {
		return nil, errors.New("invalid pricing output limit")
	}
	if inputDecimals > 36 || outputDecimals > 36 {
		return nil, errors.New("unsupported asset precision")
	}
	policy := cappedPricing{input: input, output: output}
	switch settings.Kind {
	case ReservePricing:
		if inputDecimals != outputDecimals || settings.Rate != "" {
			return nil, errors.New("fixed reserve requires equal decimals and no exchange rate")
		}
		margin, err := amount(settings.MinMargin)
		if err != nil {
			return nil, err
		}
		policy.price = func(input *big.Int) *big.Int { return new(big.Int).Sub(input, margin) }
	case RatePricing:
		if settings.MinMargin != "" || !decimalRate.MatchString(settings.Rate) {
			return nil, errors.New("fixed rate requires a positive decimal rate and no reserve")
		}
		rate, ok := new(big.Rat).SetString(settings.Rate)
		if !ok || rate.Sign() <= 0 {
			return nil, errors.New("invalid exchange rate")
		}
		scale := new(big.Rat).SetFrac(power(outputDecimals), power(inputDecimals))
		rate.Mul(rate, scale)
		policy.price = func(input *big.Int) *big.Int {
			return new(big.Int).Quo(new(big.Int).Mul(input, rate.Num()), rate.Denom())
		}
	default:
		return nil, errors.New("pricing strategy is not installed")
	}
	if _, err := policy.Output(input); err != nil {
		return nil, err
	}
	return policy, nil
}

func Admit(policy Pricing, input, output *big.Int) error {
	maximum, err := policy.Output(input)
	if err != nil {
		return err
	}
	if output.Sign() <= 0 || output.Cmp(maximum) > 0 {
		return errors.New("output exceeds pricing policy")
	}
	return nil
}

func power(decimals uint8) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
}
