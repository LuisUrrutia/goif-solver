package app

import (
	"context"
	"errors"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"github.com/LuisUrrutia/goif-solver/internal/settlement/polymer"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

func configureSettlements(c config.Config, clients map[uint64]*ethclient.Client, senders map[string]map[uint64]*evm.Sender, access bool) (map[string]settlement.Backend, error) {
	backends := make(map[string]settlement.Backend, len(c.Routes))
	proofClients := map[settlement.ID]*polymer.Client{}
	for _, route := range c.Routes {
		definition, ok := c.Settlement.Backends[route.Settlement]
		if !ok {
			return nil, errors.New("settlement backend is not configured")
		}
		var signer common.Address
		for _, s := range c.Signers {
			if s.Name == route.Signer {
				signer = s.Address
			}
		}
		switch definition.Kind {
		case config.PolymerSettlement:
			settings := definition.Polymer
			if settings == nil {
				return nil, errors.New("polymer settings absent")
			}
			if access && proofClients[route.Settlement] == nil {
				key, err := config.Secret(settings.KeyEnv)
				if err != nil {
					return nil, err
				}
				client, err := polymer.New(settings.API, key, settings.RequestMethod, settings.QueryMethod, c.RequestsPerSecond)
				if err != nil {
					return nil, err
				}
				proofClients[route.Settlement] = client
			}
			backend, err := polymer.NewBackend(route.Settlement, route, signer, clients, senders[route.Signer][route.OriginChain], proofClients[route.Settlement])
			if err != nil {
				return nil, err
			}
			backends[route.Name] = backend
		default:
			return nil, errors.New("unsupported settlement backend")
		}
	}
	return backends, nil
}

func openClients(ctx context.Context, c config.Config) (map[uint64]*ethclient.Client, func(), error) {
	clients := map[uint64]*ethclient.Client{}
	closeClients := func() {
		for _, client := range clients {
			client.Close()
		}
	}
	for _, chain := range c.Chains {
		endpoints, err := chain.URLs()
		if err != nil {
			closeClients()
			return nil, nil, err
		}
		client, err := evm.NewClient(ctx, endpoints, chain.ID, c.RequestsPerSecond)
		if err != nil {
			closeClients()
			return nil, nil, err
		}
		clients[chain.ID] = client
	}
	return clients, closeClients, nil
}
