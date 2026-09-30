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

	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/preflight"
	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	"github.com/LuisUrrutia/goif-solver/internal/quote"
	"go.uber.org/zap"
)

type providerSet struct {
	stream    func(config.Source) (intent.Source, error)
	publisher quote.Publisher
	verify    func(context.Context, config.Route) error
	catalog   func(context.Context) error
	register  func(context.Context) error
	authorize func() error
	history   func(context.Context, intent.Identity, bool) (preflight.IntentReport, error)
}

func lifiProvider(c config.Config, definition config.Provider, log *zap.Logger) (providerSet, error) {
	settings, err := config.Decode[lifiSettings](definition.Settings)
	if err != nil {
		return providerSet{}, fmt.Errorf("LI.FI settings: %w", err)
	}
	if settings.API == "" || !config.ValidEnv(settings.KeyEnv) {
		return providerSet{}, errors.New("invalid LI.FI settings")
	}
	d, err := boundLIFIDeployment(c, definition.Routes)
	if err != nil {
		return providerSet{}, err
	}
	api, err := lifi.New(settings.API, os.Getenv(settings.KeyEnv), settings.rate(c.RequestsPerSecond))
	if err != nil {
		return providerSet{}, err
	}
	verify := func(ctx context.Context, binding config.Route) error {
		bound, err := boundLIFIDeployment(c, []config.Route{binding})
		if err != nil {
			return err
		}
		identities, err := api.Identities(ctx)
		if err != nil {
			return err
		}
		for _, signer := range bound.Signers {
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
		for _, route := range bound.Routes {
			if !contains(contracts.Input, route.OriginChain, route.InputSettler.Hex()) || !contains(contracts.Output, route.DestinationChain, route.OutputSettler.Hex()) {
				return errors.New("route contracts not registered; run register first")
			}
		}
		return nil
	}

	result := providerSet{verify: verify, catalog: func(ctx context.Context) error { return api.CheckCatalog(ctx, d.Routes) }, stream: func(source config.Source) (intent.Source, error) {
		stream, err := config.Decode[streamSettings](source.Settings)
		if err != nil {
			return nil, fmt.Errorf("LI.FI stream settings: %w", err)
		}
		if stream.KeyEnv != "" && !config.ValidEnv(stream.KeyEnv) {
			return nil, errors.New("invalid LI.FI stream settings")
		}
		if err := lifi.ValidateStreamURL(stream.URL); err != nil {
			return nil, err
		}
		filters := []url.Values{}
		pairs := map[[2]uint64]bool{}
		for _, route := range d.Routes {
			pair := [2]uint64{route.OriginChain, route.DestinationChain}
			if pairs[pair] {
				continue
			}
			pairs[pair] = true
			for _, status := range []string{"Signed", "Open"} {
				filter := url.Values{"status": {status}, "originChainId": {strconv.FormatUint(route.OriginChain, 10)}, "destinationChainId": {strconv.FormatUint(route.DestinationChain, 10)}}
				if len(c.IntentAllowlist) == 1 && c.IntentAllowlist[0].Kind == escrowprotocol.IntentKind {
					filter.Set("onChainOrderId", c.IntentAllowlist[0].NativeID)
				}
				filters = append(filters, filter)
			}
		}
		return &lifi.Stream{URL: stream.URL, Key: os.Getenv(stream.KeyEnv), API: api, Log: log, Filters: filters, Resolve: func(ctx context.Context, envelope lifi.Envelope) (intent.Candidate, error) {
			data := envelope.Intent()
			order, err := escrowprotocol.Parse(data.Order)
			if err != nil {
				return intent.Candidate{}, intent.ErrRejected
			}
			inputSettler, err := evm.Address(data.InputSettler)
			if err != nil {
				return intent.Candidate{}, intent.ErrRejected
			}
			matched := false
			for _, route := range d.Routes {
				if !order.MatchesRoute(inputSettler, route) {
					continue
				}
				matched = true
				if data.ID == "" {
					id, err := escrowprotocol.Identifier(order, route.InputSettler)
					if err != nil {
						return intent.Candidate{}, intent.ErrRejected
					}
					data.ID = id.Hex()
				}
				break
			}
			if !matched {
				return intent.Candidate{}, intent.ErrRejected
			}
			payload, err := json.Marshal(data)
			return intent.Candidate{ID: data.ID, Kind: escrowprotocol.IntentKind, Payload: payload}, err
		}}, nil
	}}
	result.publisher = api
	result.authorize = func() error { _, err := config.Secret(settings.KeyEnv); return err }
	result.register = func(ctx context.Context) error { return registerLIFI(ctx, d, settings, api) }
	result.history = func(ctx context.Context, id intent.Identity, access bool) (preflight.IntentReport, error) {
		if id.Kind != escrowprotocol.IntentKind {
			return preflight.IntentReport{}, errors.New("LI.FI history does not support this protocol")
		}
		envelope, err := api.Order(ctx, id.NativeID)
		if err != nil {
			return preflight.IntentReport{}, err
		}
		if !strings.EqualFold(envelope.Meta.ID, id.NativeID) {
			return preflight.IntentReport{}, errors.New("history returned another intent")
		}
		clients, closeClients, err := openClients(ctx, d, c.RequestsPerSecond)
		if err != nil {
			return preflight.IntentReport{}, err
		}
		defer closeClients()
		return auditEscrow(ctx, c, d, envelope.Intent(), envelope.Meta.Status, envelope.Meta.FillTx, clients, access)
	}
	return result, nil
}

const lifiKind config.Kind = "lifi"

type lifiSettings struct {
	API               string `json:"api"`
	KeyEnv            string `json:"key_env"`
	RequestsPerSecond int    `json:"requests_per_second,omitempty"`
}

func (s lifiSettings) rate(fallback int) int {
	if s.RequestsPerSecond != 0 {
		return s.RequestsPerSecond
	}
	return fallback
}

type streamSettings struct {
	URL    string `json:"url"`
	KeyEnv string `json:"key_env,omitempty"`
}

func configureProviders(c config.Config, executions map[intent.Kind]*Execution, log *zap.Logger) (map[string]providerSet, error) {
	selected := map[string]bool{}
	for _, source := range c.Sources {
		if source.Provider != "" {
			selected[source.Provider] = true
		}
	}
	for _, publication := range c.Publications {
		selected[publication.Provider] = true
	}
	result := map[string]providerSet{}
	for name := range selected {
		definition := c.Providers[name]
		for _, route := range definition.Routes {
			if executions[route.Protocol] == nil || executions[route.Protocol].Quotes[route.Name] == nil {
				return nil, errors.New("provider route does not exist")
			}
		}
		provider, err := openProvider(c, definition, log)
		if err != nil {
			return nil, err
		}
		result[name] = provider
	}
	return result, nil
}

func boundLIFIDeployment(c config.Config, routes []config.Route) (escrowprotocol.Deployment, error) {
	var selected escrowprotocol.Deployment
	deployment, err := config.Decode[escrowprotocol.Deployment](c.Executions[escrowprotocol.IntentKind])
	if err != nil {
		return selected, fmt.Errorf("LI.FI escrow binding: %w", err)
	}
	signers, chains := map[string]bool{}, map[uint64]bool{}
	for _, binding := range routes {
		if binding.Protocol != escrowprotocol.IntentKind {
			return selected, errors.New("LI.FI binding requires its supported escrow protocol")
		}
		found := false
		for _, route := range deployment.Routes {
			if route.Name == binding.Name {
				selected.Routes = append(selected.Routes, route)
				signers[route.Signer] = true
				chains[route.OriginChain] = true
				chains[route.DestinationChain] = true
				found = true
				break
			}
		}
		if !found {
			return selected, errors.New("unknown LI.FI route binding")
		}
	}
	for _, signer := range deployment.Signers {
		if signers[signer.Name] {
			selected.Signers = append(selected.Signers, signer)
		}
	}
	for _, chain := range deployment.Chains {
		if chains[chain.ID] {
			selected.Chains = append(selected.Chains, chain)
		}
	}
	return selected, nil
}

func openProvider(c config.Config, definition config.Provider, log *zap.Logger) (providerSet, error) {
	switch definition.Kind {
	case lifiKind:
		return lifiProvider(c, definition, log)
	default:
		return providerSet{}, errors.New("provider adapter not installed")
	}
}
