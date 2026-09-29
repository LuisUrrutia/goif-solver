package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/oif"
	oifescrow "github.com/LuisUrrutia/goif-solver/internal/oif/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/preflight"
	evmpreflight "github.com/LuisUrrutia/goif-solver/internal/preflight/evm"
	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/quote"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"go.uber.org/zap"
)

func builtins() map[intent.Kind]Factory {
	return map[intent.Kind]Factory{escrowprotocol.IntentKind: EscrowFactory(evm.Custodies())}
}

func EscrowFactory(custodies map[evm.CustodyKind]evm.CustodyFactory) Factory {
	return func(ctx context.Context, c config.Config, raw json.RawMessage, store coordination.Backend, execute bool, log *zap.Logger) (*Execution, error) {
		d, err := config.Decode[escrowprotocol.Deployment](raw)
		if err != nil {
			return nil, err
		}
		if err = d.Validate(); err != nil {
			return nil, err
		}
		clients, closeClients, err := openClients(ctx, d, c.RequestsPerSecond)
		if err != nil {
			return nil, err
		}
		success := false
		defer func() {
			if !success {
				closeClients()
			}
		}()
		engine := &escrow.Engine{Config: escrow.Policy{Deployment: d, Version: c.Version, IntentAllowlist: c.IntentAllowlist}, Store: store, Execute: execute, Clients: clients, Senders: map[string]map[uint64]*evm.Sender{}}
		plans := map[string]evm.CustodyPlan{}
		used := map[string]bool{}
		for _, route := range d.Routes {
			used[route.Signer] = true
		}
		for _, definition := range d.Signers {
			if !used[definition.Name] {
				continue
			}
			plan, err := evm.CompileSigner(definition, custodies)
			if err != nil {
				return nil, err
			}
			plans[definition.Name] = plan
		}
		if execute {
			for _, definition := range d.Signers {
				plan, ok := plans[definition.Name]
				if !ok {
					continue
				}
				signer, err := plan.Open(ctx)
				if err != nil {
					return nil, err
				}
				engine.Senders[definition.Name] = map[uint64]*evm.Sender{}
				for _, chain := range d.Chains {
					allowed := false
					for _, id := range definition.Chains {
						allowed = allowed || id == chain.ID
					}
					if !allowed || clients[chain.ID] == nil {
						continue
					}
					cap, err := evm.Uint(chain.MaxFeeWei, 256)
					if err != nil {
						return nil, err
					}
					engine.Senders[definition.Name][chain.ID] = &evm.Sender{Log: log, Client: clients[chain.ID], Store: store, Signer: signer, Policy: evm.SendPolicy{Enabled: chain.SigningEnabled, Chain: chain.ID, Confirmations: chain.Confirmations, MaxGas: chain.MaxGas, MaxFee: cap}}
				}
			}
		}
		engine.Settlements, err = configureSettlements(c, d, clients, engine.Senders, execute)
		if err != nil {
			return nil, err
		}
		verifier := &evmpreflight.RouteVerifier{Clients: clients, Settlements: engine.Settlements}
		engine.Verifier = verifier
		quotes, err := configureQuoteSources(d, clients)
		if err != nil {
			return nil, err
		}
		sources := map[string]quote.Source{}
		for name, source := range quotes {
			sources[name] = source
		}
		policy, err := escrowPolicy(c, d, plans)
		if err != nil {
			return nil, err
		}
		result := &Execution{Executor: engine, Checker: &evmpreflight.Checker{Config: d, Clients: clients, Verifier: verifier}, Quotes: sources, Policy: policy, Close: closeClients}
		result.Source = func(source config.Source) (intent.Source, error) { return escrowSource(source, d, clients, store) }
		result.Audit = func(ctx context.Context, record coordination.Record, access bool) (preflight.IntentReport, error) {
			var candidate intent.Candidate
			var work escrow.Work
			var durable intent.Progress
			var progress escrow.Progress
			if json.Unmarshal([]byte(record.Payload), &candidate) != nil || candidate.Kind != escrowprotocol.IntentKind || candidate.Identity().Key() != record.ID || json.Unmarshal(candidate.Payload, &work) != nil || json.Unmarshal([]byte(record.Detail), &durable) != nil || json.Unmarshal(durable.State, &progress) != nil || progress.Fill == nil {
				return preflight.IntentReport{}, errors.New("durable intent has no valid fulfillment evidence")
			}
			return auditEscrow(ctx, c, d, work.Envelope, string(record.Stage), progress.Fill.Log.TxHash.Hex(), clients, access)
		}
		result.OIF = map[string]oif.Route{}
		for _, route := range d.Routes {
			var signer common.Address
			var gas uint64
			for _, definition := range d.Signers {
				if definition.Name == route.Signer {
					signer = definition.Address
				}
			}
			for _, chain := range d.Chains {
				if chain.ID == route.OriginChain {
					gas = chain.MaxGas
				}
			}
			result.OIF[route.Name] = &oifescrow.Route{Policy: route, Signer: signer, Gas: gas, Inventory: quotes[route.Name], Verify: verifier.Verify}
		}
		success = true
		return result, nil
	}
}

type logSettings struct {
	Settler         string `json:"settler"`
	ChainID         uint64 `json:"chain_id"`
	StartBlock      uint64 `json:"start_block,omitempty"`
	Lookback        uint64 `json:"lookback,omitempty"`
	IntervalSeconds int    `json:"interval_seconds"`
}

func escrowSource(source config.Source, d escrowprotocol.Deployment, clients map[uint64]*ethclient.Client, store escrowprotocol.Checkpoints) (intent.Source, error) {
	settings, err := config.Decode[logSettings](source.Settings)
	if err != nil {
		return nil, err
	}
	settler, err := evm.Address(settings.Settler)
	if err != nil || settings.IntervalSeconds < 1 || settings.IntervalSeconds > 60 || settings.Lookback > 10000 {
		return nil, errors.New("invalid escrow log source")
	}
	found := false
	for _, route := range d.Routes {
		found = found || route.OriginChain == settings.ChainID && route.InputSettler == settler
	}
	if !found {
		return nil, errors.New("log source must monitor a configured input settler")
	}
	for _, chain := range d.Chains {
		if chain.ID == settings.ChainID {
			return &escrowprotocol.LogSource{Client: clients[chain.ID], Checkpoints: store, Settler: settler, ChainID: chain.ID, Confirmations: chain.Confirmations, StartBlock: settings.StartBlock, Lookback: settings.Lookback, Interval: time.Duration(settings.IntervalSeconds) * time.Second}, nil
		}
	}
	return nil, errors.New("log source network not configured")
}

func escrowPolicy(c config.Config, d escrowprotocol.Deployment, plans map[string]evm.CustodyPlan) (json.RawMessage, error) {
	policy := escrowprotocol.Deployment{Routes: slices.Clone(d.Routes)}
	usedChains, usedSigners := map[uint64]bool{}, map[string]bool{}
	for _, route := range d.Routes {
		usedChains[route.OriginChain] = true
		usedChains[route.DestinationChain] = true
		usedSigners[route.Signer] = true
	}
	for _, chain := range d.Chains {
		if !usedChains[chain.ID] {
			continue
		}
		chain.RequestsPerSecond = 0
		chain.RPCs = nil
		policy.Chains = append(policy.Chains, chain)
	}
	for _, signer := range d.Signers {
		if !usedSigners[signer.Name] {
			continue
		}
		signer.Chains = slices.Clone(signer.Chains)
		slices.Sort(signer.Chains)
		signer.Custody.Settings = plans[signer.Name].Policy
		policy.Signers = append(policy.Signers, signer)
	}
	slices.SortFunc(policy.Chains, func(a, b evm.Chain) int { return cmp.Compare(a.ID, b.ID) })
	slices.SortFunc(policy.Signers, func(a, b evm.SignerConfig) int { return strings.Compare(a.Name, b.Name) })
	slices.SortFunc(policy.Routes, func(a, b escrowprotocol.Route) int { return strings.Compare(a.Name, b.Name) })
	backends, err := settlementPolicies(c, d.Routes)
	if err != nil {
		return nil, err
	}
	for i := range policy.Routes {
		policy.Routes[i].InputSymbol = ""
		policy.Routes[i].OutputSymbol = ""
	}

	return json.Marshal(struct {
		Settlements map[settlement.ID]json.RawMessage
		Profile     string
		Deployment  escrowprotocol.Deployment
	}{Profile: "lifi-escrow-deployment-v1", Deployment: policy, Settlements: backends})
}
