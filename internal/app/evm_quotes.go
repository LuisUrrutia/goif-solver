package app

import (
	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"

	"github.com/LuisUrrutia/goif-solver/internal/quote"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

func configureQuoteSources(c escrowprotocol.Deployment, clients map[uint64]*ethclient.Client) ([]quote.Binding, error) {
	sources := make([]quote.Binding, 0, len(c.Routes))
	for _, route := range c.Routes {
		var signer common.Address
		for _, definition := range c.Signers {
			if definition.Name == route.Signer {
				signer = definition.Address
			}
		}
		source, err := escrowprotocol.NewQuoteSource(route, signer, clients[route.DestinationChain])
		if err != nil {
			return nil, err
		}
		sources = append(sources, quote.Binding{Name: route.Name, Source: source})
	}
	return sources, nil
}
