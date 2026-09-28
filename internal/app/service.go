package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	"github.com/LuisUrrutia/goif-solver/internal/polymer"
	"github.com/LuisUrrutia/goif-solver/internal/preflight"
	"github.com/LuisUrrutia/goif-solver/internal/solver"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func New(ctx context.Context, c config.Config, node string, execute bool, log *zap.Logger) (*solver.Service, error) {
	if node == "" || len(node) > 128 {
		return nil, errors.New("node ID required")
	}
	redisURL, err := config.Secret(c.RedisEnv)
	if err != nil {
		return nil, err
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, errors.New("invalid Redis URL")
	}
	options.DialTimeout = 5 * time.Second
	options.ReadTimeout = 5 * time.Second
	options.WriteTimeout = 5 * time.Second
	client := redis.NewClient(options)
	store, err := coordination.New(client, c.Namespace)
	if err != nil {
		client.Close()
		return nil, err
	}
	engine := &escrow.Engine{Config: c, Store: store, Execute: execute, Clients: map[uint64]*ethclient.Client{}, Senders: map[string]map[uint64]*evm.Sender{}}
	service := &solver.Service{Engine: &solver.Engine{Store: store, Executors: map[intent.Kind]solver.Executor{evm.IntentKind: engine}}, Log: log, Node: node, Workers: c.Workers, Interval: time.Duration(c.WorkIntervalSeconds) * time.Second}
	service.Shutdown = func() {
		for _, rpc := range engine.Clients {
			rpc.Close()
		}
		client.Close()
	}
	var api *lifi.Client
	ok := false
	defer func() {
		if !ok {
			service.Close()
		}
	}()
	if err = store.Ping(ctx); err != nil {
		return nil, errors.New("Redis unavailable")
	}
	apiKey := os.Getenv(c.APIKeyEnv)
	if execute && apiKey == "" {
		return nil, fmt.Errorf("required environment variable %s is unset", c.APIKeyEnv)
	}
	api, err = lifi.New(c.OrderAPI, apiKey, c.RequestsPerSecond)
	if err != nil {
		return nil, err
	}

	for _, chain := range c.Chains {
		endpoint, err := chain.URLs()
		if err != nil {
			return nil, err
		}
		rpc, err := evm.NewClient(ctx, endpoint, chain.ID, c.RequestsPerSecond)
		if err != nil {
			return nil, err
		}
		engine.Clients[chain.ID] = rpc
	}
	engine.Verifier = &preflight.RouteVerifier{Clients: engine.Clients}
	service.Sources, err = sources(c, store, engine.Clients, api)
	if err != nil {
		return nil, err
	}
	service.Quotes = &Quoter{Config: c, Engine: engine, API: api}

	if execute {
		identities, err := api.Identities(ctx)
		if err != nil {
			return nil, err
		}
		for _, signer := range c.Signers {
			found := false
			for _, identity := range identities {
				found = found || strings.EqualFold(identity, signer.Address.Hex())
			}
			if !found {
				return nil, errors.New("solver identity not registered; run register first")
			}
		}
		contracts, err := api.SupportedContracts(ctx)
		if err != nil {
			return nil, err
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
				return nil, errors.New("route contracts not registered; run register first")
			}
		}
	}
	if execute {
		key, err := config.Secret(c.PolymerKeyEnv)
		if err != nil {
			return nil, err
		}
		engine.Proofs, err = polymer.New(c.PolymerAPI, key, c.PolymerRequest, c.PolymerQuery, c.RequestsPerSecond)
		if err != nil {
			return nil, err
		}
		for _, signerConfig := range c.Signers {
			secret, err := config.Secret(signerConfig.KeyEnv)
			if err != nil {
				return nil, err
			}
			signer, err := evm.NewLocalSigner(secret, signerConfig.Address, signerConfig.Chains)
			if err != nil {
				return nil, err
			}
			engine.Senders[signerConfig.Name] = map[uint64]*evm.Sender{}
			for _, chain := range c.Chains {
				allowed := false
				for _, id := range signerConfig.Chains {
					allowed = allowed || id == chain.ID
				}
				if !allowed {
					continue
				}
				cap, _ := evm.Uint(chain.MaxFeeWei, 256)
				engine.Senders[signerConfig.Name][chain.ID] = &evm.Sender{Log: log, Client: engine.Clients[chain.ID], Store: store, Signer: signer, Policy: evm.SendPolicy{Enabled: chain.SigningEnabled, Chain: chain.ID, Confirmations: chain.Confirmations, MaxGas: chain.MaxGas, MaxFee: cap}}
			}
		}
	}
	// Listen addresses are local. The rest of the public policy must match fleet-wide.
	policy := c
	policy.Listen = ""
	b, _ := json.Marshal(policy)
	digest := sha256.Sum256(b)
	if err = store.BindConfig(ctx, hex.EncodeToString(digest[:])); err != nil {
		return nil, errors.New("fleet configuration differs; drain and migrate namespace")
	}
	ok = true
	return service, nil
}
