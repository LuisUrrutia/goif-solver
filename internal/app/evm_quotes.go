package app

import (
	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

func configureQuoteSources(c escrowprotocol.Deployment, clients map[uint64]*ethclient.Client) (map[string]*escrowprotocol.QuoteSource, error) {
	sources := make(map[string]*escrowprotocol.QuoteSource, len(c.Routes))
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
		sources[route.Name] = source
	}
	return sources, nil
}
