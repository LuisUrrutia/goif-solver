package app

import (
	"context"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/quote"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

func OpenQuoteSources(ctx context.Context, c config.Config) ([]quote.Binding, func(), error) {
	clients, closeClients, err := openClients(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	sources, err := configureQuoteSources(c, clients)
	if err != nil {
		closeClients()
		return nil, nil, err
	}
	return sources, closeClients, nil
}

func configureQuoteSources(c config.Config, clients map[uint64]*ethclient.Client) ([]quote.Binding, error) {
	sources := make([]quote.Binding, 0, len(c.Routes))
	for _, route := range c.Routes {
		var signer common.Address
		for _, definition := range c.Signers {
			if definition.Name == route.Signer {
				signer = definition.Address
			}
		}
		source, err := evm.NewQuoteSource(route, signer, clients[route.DestinationChain])
		if err != nil {
			return nil, err
		}
		sources = append(sources, quote.Binding{Name: route.Name, Source: source})
	}
	return sources, nil
}
