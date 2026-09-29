package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	"github.com/LuisUrrutia/goif-solver/internal/quote"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

type providerSet struct {
	stream    func(config.IntentSource) (intent.Source, error)
	publisher quote.Publisher
	verify    func(context.Context) error
	catalog   func(context.Context) error
}

func configureProviders(c config.Config, clients map[uint64]*ethclient.Client) (providerSet, error) {
	selected := c.QuotePublisher == config.LIFIPublisher
	for _, source := range c.IntentSources {
		selected = selected || source.Kind == config.LIFIWebSocket
	}
	if !selected {
		return providerSet{}, nil
	}
	if c.Providers.LIFI == nil {
		return providerSet{}, errors.New("LI.FI provider is not configured")
	}
	api, err := lifi.New(c.Providers.LIFI.API, os.Getenv(c.Providers.LIFI.KeyEnv), c.RequestsPerSecond)
	if err != nil {
		return providerSet{}, err
	}
	verify := func(ctx context.Context) error {
		identities, err := api.Identities(ctx)
		if err != nil {
			return err
		}
		for _, signer := range c.Signers {
			found := false
			for _, identity := range identities {
				found = found || strings.EqualFold(identity, signer.Address.Hex())
			}
			if !found {
				return errors.New("solver identity not registered; run register first")
			}
		}
		contracts, err := api.SupportedContracts(ctx)
		if err != nil {
			return err
		}
		contains := func(list []lifi.Contract, chain uint64, address string) bool {
			for _, contract := range list {
				if contract.Chain == fmt.Sprintf("eip155:%d", chain) && strings.EqualFold(contract.Address, address) {
					return true
				}
			}
			return false
		}
		for _, route := range c.Routes {
			if !contains(contracts.Input, route.OriginChain, route.InputSettler.Hex()) || !contains(contracts.Output, route.DestinationChain, route.OutputSettler.Hex()) {
				return errors.New("route contracts not registered; run register first")
			}
		}
		return nil
	}

	result := providerSet{verify: verify, catalog: func(ctx context.Context) error { return api.CheckCatalog(ctx, c.Routes) }, stream: func(source config.IntentSource) (intent.Source, error) {
		if err := lifi.ValidateStreamURL(source.URL); err != nil {
			return nil, err
		}
		filters := []url.Values{}
		pairs := map[[2]uint64]bool{}
		for _, route := range c.Routes {
			pair := [2]uint64{route.OriginChain, route.DestinationChain}
			if pairs[pair] {
				continue
			}
			pairs[pair] = true
			for _, status := range []string{"Signed", "Open"} {
				filter := url.Values{"status": {status}, "originChainId": {strconv.FormatUint(route.OriginChain, 10)}, "destinationChainId": {strconv.FormatUint(route.DestinationChain, 10)}}
				if len(c.IntentAllowlist) == 1 {
					filter.Set("onChainOrderId", c.IntentAllowlist[0].Hex())
				}
				filters = append(filters, filter)
			}
		}
		return &lifi.Stream{URL: source.URL, Key: os.Getenv(source.KeyEnv), API: api, Filters: filters, Resolve: func(ctx context.Context, envelope lifi.Envelope) (intent.Candidate, error) {
			data := envelope.Intent()
			if data.ID == "" {
				order, err := evm.Parse(data.Order)
				if err != nil {
					return intent.Candidate{}, intent.ErrRejected
				}
				matched := false
				for _, route := range c.Routes {
					if order.OriginChainId.Uint64() != route.OriginChain || !common.IsHexAddress(data.InputSettler) || common.HexToAddress(data.InputSettler) != route.InputSettler {
						continue
					}
					// Submit notifications may omit server metadata. Ask only a configured
					// settler for the identifier; normal policy validation still precedes spend.
					values, err := evm.Call(ctx, clients[route.OriginChain], route.InputSettler, evm.InputABI, nil, "orderIdentifier", order)
					if err != nil {
						return intent.Candidate{}, err
					}
					id, ok := values[0].([32]byte)
					if !ok {
						return intent.Candidate{}, errors.New("invalid on-chain identifier")
					}
					data.ID = common.Hash(id).Hex()
					matched = true
					break
				}
				if !matched {
					return intent.Candidate{}, intent.ErrRejected
				}
			}
			payload, err := json.Marshal(data)
			return intent.Candidate{ID: data.ID, Kind: evm.IntentKind, Payload: payload}, err
		}}, nil
	}}
	if c.QuotePublisher == config.LIFIPublisher {
		result.publisher = api
	}
	return result, nil
}
