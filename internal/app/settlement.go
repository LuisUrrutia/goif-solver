package app

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"github.com/LuisUrrutia/goif-solver/internal/settlement/polymer"
	polymerevm "github.com/LuisUrrutia/goif-solver/internal/settlement/polymer/evm"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

func configureSettlements(c config.Config, d escrowprotocol.Deployment, clients map[uint64]*ethclient.Client, senders map[string]map[uint64]*evm.Sender, access bool) (map[string]settlement.Backend, error) {
	backends := make(map[string]settlement.Backend, len(d.Routes))
	proofClients := map[settlement.ID]*polymer.Client{}
	for _, route := range d.Routes {
		definition, ok := c.Settlements[route.Settlement]
		if !ok {
			return nil, errors.New("settlement backend is not configured")
		}
		var signer common.Address
		for _, s := range d.Signers {
			if s.Name == route.Signer {
				signer = s.Address
			}
		}
		switch definition.Kind {
		case polymerKind:
			settings, err := config.Decode[polymerSettings](definition.Settings)
			if err != nil || !config.ValidEnv(settings.KeyEnv) {
				return nil, errors.New("polymer settings absent")
			}
			rate := settings.RequestsPerSecond
			if rate == 0 {
				rate = c.RequestsPerSecond
			}
			if _, err := polymer.New(settings.API, "", settings.RequestMethod, settings.QueryMethod, rate); err != nil {
				return nil, err
			}
			if access && proofClients[route.Settlement] == nil {
				key, err := config.Secret(settings.KeyEnv)
				if err != nil {
					return nil, err
				}
				client, err := polymer.New(settings.API, key, settings.RequestMethod, settings.QueryMethod, rate)
				if err != nil {
					return nil, err
				}
				proofClients[route.Settlement] = client
			}
			backend, err := polymerevm.NewBackend(route.Settlement, route, signer, clients, senders[route.Signer][route.OriginChain], proofClients[route.Settlement])
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

func openClients(ctx context.Context, c escrowprotocol.Deployment, rate int) (map[uint64]*ethclient.Client, func(), error) {
	clients := map[uint64]*ethclient.Client{}
	closeClients := func() {
		for _, client := range clients {
			client.Close()
		}
	}
	used := map[uint64]bool{}
	for _, route := range c.Routes {
		used[route.OriginChain] = true
		used[route.DestinationChain] = true
	}
	for _, chain := range c.Chains {
		if !used[chain.ID] {
			continue
		}
		endpoints, err := chain.Endpoints()
		if err != nil {
			closeClients()
			return nil, nil, err
		}
		client, err := evm.NewClient(ctx, endpoints, chain.ID, rate)
		if err != nil {
			closeClients()
			return nil, nil, err
		}
		clients[chain.ID] = client
	}
	return clients, closeClients, nil
}

const polymerKind config.Kind = "polymer"

type polymerSettings struct {
	API               string `json:"api"`
	KeyEnv            string `json:"key_env"`
	RequestMethod     string `json:"request_method"`
	QueryMethod       string `json:"query_method"`
	RequestsPerSecond int    `json:"requests_per_second,omitempty"`
}

func settlementPolicies(c config.Config, routes []escrowprotocol.Route) (map[settlement.ID]json.RawMessage, error) {
	result := map[settlement.ID]json.RawMessage{}
	for _, route := range routes {
		definition := c.Settlements[route.Settlement]
		switch definition.Kind {
		case polymerKind:
			settings, err := config.Decode[polymerSettings](definition.Settings)
			if err != nil {
				return nil, err
			}
			raw, err := json.Marshal(struct {
				Kind                       config.Kind
				RequestMethod, QueryMethod string
			}{Kind: definition.Kind, RequestMethod: settings.RequestMethod, QueryMethod: settings.QueryMethod})
			if err != nil {
				return nil, err
			}
			result[route.Settlement] = raw
		default:
			return nil, errors.New("settlement policy adapter not installed")
		}
	}
	return result, nil
}
