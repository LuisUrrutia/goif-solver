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
		Solver          string
		InputValidator  string
		OutputValidator string
		Input           Asset
		Output          Asset
		Ranges          []PriceRange
		Expiry          int64
	}
)

type Publisher interface {
	PublishOffer(context.Context, Offer) error
}

// Source owns inventory checks and offer construction for one configured route.
// A withdrawal must preserve route identity without requiring an inventory read.
type Source interface {
	Offer(context.Context, bool) (Offer, error)
}

type Binding struct {
	Source    Source
	Publisher Publisher
	Name      string
}
