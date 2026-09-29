// Package quote models standing offers independently of their publication API.
package quote

import "context"

type Asset struct {
	Chain    string
	Address  string
	Decimals uint8
}
type (
	PriceRange struct{ Minimum, Maximum, Rate string }
	Offer      struct {
		Input           Asset
		Output          Asset
		Ranges          []PriceRange
		Solver          string
		InputValidator  string
		OutputValidator string
		Expiry          int64
	}
)

type Publisher interface {
	PublishOffer(context.Context, Offer) error
}
