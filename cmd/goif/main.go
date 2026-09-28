package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/control"
	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	"github.com/LuisUrrutia/goif-solver/internal/polymer"
	"github.com/LuisUrrutia/goif-solver/internal/preflight"
	"github.com/LuisUrrutia/goif-solver/internal/solver"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: goif {preflight|proof-check|run|register|publish|withdraw|status|control} -config config/sepolia.json")
	}
	command := os.Args[1]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	path := flags.String("config", "config/sepolia.json", "public configuration file")
	node := flags.String("node", os.Getenv("HOSTNAME"), "unique node ID")
	execute := flags.Bool("execute-testnet", false, "authorize testnet order execution with injected keys")
	publish := flags.Bool("publish-quotes", false, "publish and renew configured testnet inventory quotes")
	authorizeRegistration := flags.Bool("authorize-registration", false, "authorize identity signatures and supported-contract registration")
	order := flags.String("order", "", "on-chain order ID for status")
	controlPath := flags.String("control-file", "", "versioned operational control JSON to apply")
	if err := flags.Parse(os.Args[2:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	c, err := config.Load(*path)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if command == "preflight" {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		report, err := preflight.Run(ctx, c)
		if err != nil {
			return err
		}
		if *order != "" {
			audit, err := preflight.AuditOrder(ctx, c, *order)
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(struct {
				Preflight preflight.Report      `json:"preflight"`
				Order     preflight.OrderReport `json:"order"`
			}{report, audit})
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	if command == "proof-check" {
		return checkProofAccess(ctx, c, *order)
	}
	if command == "status" || command == "control" {
		return storedCommand(ctx, c, command, *order, *controlPath)
	}
	if command == "register" {
		if !*authorizeRegistration {
			return errors.New("registration requires -authorize-registration")
		}
		return register(ctx, c)
	}
	if command == "publish" {
		if !*publish {
			return errors.New("publication requires -publish-quotes")
		}
		return publishOnly(ctx, c)
	}
	if command != "run" && command != "withdraw" {
		return errors.New("unknown command")
	}
	if *publish && !*execute {
		return errors.New("quote publication requires -execute-testnet")
	}
	if command == "withdraw" {
		key, err := config.Secret(c.APIKeyEnv)
		if err != nil {
			return err
		}
		api, err := lifi.New(c.OrderAPI, key, c.RequestsPerSecond)
		if err != nil {
			return err
		}
		service := solver.Service{Engine: &solver.Engine{Config: c}, API: api}
		for _, route := range c.Routes {
			quote, err := service.Quote(route, true)
			if err != nil {
				return err
			}
			if err = api.Withdraw(ctx, quote); err != nil {
				return err
			}
			if err = api.VerifyQuote(ctx, quote); err != nil {
				return err
			}
		}
		return nil
	}
	if *order != "" {
		if _, err := evm.Word(*order); err != nil {
			return errors.New("invalid execution order ID")
		}
		c.OrderAllowlist = []common.Hash{common.HexToHash(*order)}
	}
	preflightCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	_, err = preflight.Run(preflightCtx, c)
	cancel()
	if err != nil {
		return err
	}
	if *node == "" {
		*node, err = os.Hostname()
		if err != nil {
			return err
		}
	}
	log, err := zap.NewProduction()
	if err != nil {
		return err
	}
	defer func() { _ = log.Sync() }()
	service, err := solver.New(ctx, c, *node, *execute, log)
	if err != nil {
		return err
	}
	defer service.Close()
	service.Publish = *publish
	token := os.Getenv(c.ControlTokenEnv)
	if !strings.HasPrefix(c.Listen, "127.0.0.1:") && len(token) < 32 {
		return errors.New("non-loopback HTTP requires a control token of at least 32 characters")
	}
	server := &http.Server{Addr: c.Listen, Handler: control.Handler(service, token), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	engineDone := make(chan struct{})
	go func() { defer close(engineDone); _ = service.Run(runCtx) }()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ListenAndServe() }()
	log.Info("solver started", zap.String("node", *node), zap.Bool("execute_testnet", *execute), zap.Bool("publish_quotes", *publish))
	select {
	case <-ctx.Done():
	case err = <-serverDone:
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}
	cancelRun()
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	_ = server.Shutdown(shutdown)
	<-engineDone
	if *publish {
		if withdrawErr := service.PublishQuotes(shutdown, true); withdrawErr != nil {
			log.Warn("shutdown quote withdrawal failed", zap.Error(withdrawErr))
		}
	}
	return err
}
func storedCommand(ctx context.Context, c config.Config, command, id, path string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	endpoint, err := config.Secret(c.RedisEnv)
	if err != nil {
		return err
	}
	options, err := redis.ParseURL(endpoint)
	if err != nil {
		return errors.New("invalid Redis URL")
	}
	client := redis.NewClient(options)
	defer client.Close()
	store, err := coordination.New(client, c.Namespace)
	if err != nil {
		return err
	}
	if command == "status" {
		if _, err := evm.Word(id); err != nil {
			return errors.New("status requires a valid -order ID")
		}
		record, err := store.Record(ctx, strings.ToLower(id))
		if err != nil {
			return errors.New("order record unavailable")
		}
		return json.NewEncoder(os.Stdout).Encode(record)
	}
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return errors.New("read control file failed")
		}
		var c coordination.Control
		if len(b) > 64<<10 || json.Unmarshal(b, &c) != nil || c.Version == 0 {
			return errors.New("invalid control file")
		}
		if err = store.SetControl(ctx, c.Version-1, c); err != nil {
			return err
		}
	}
	state, err := store.Control(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(state)
}
func register(ctx context.Context, c config.Config) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if _, err := preflight.Run(ctx, c); err != nil {
		return err
	}
	key, err := config.Secret(c.APIKeyEnv)
	if err != nil {
		return err
	}
	api, err := lifi.New(c.OrderAPI, key, c.RequestsPerSecond)
	if err != nil {
		return err
	}
	identities, err := api.Identities(ctx)
	if err != nil {
		return err
	}
	for _, definition := range c.Signers {
		exists := false
		for _, id := range identities {
			exists = exists || strings.EqualFold(id, definition.Address.Hex())
		}
		secret, err := config.Secret(definition.KeyEnv)
		if err != nil {
			return err
		}
		signer, err := evm.NewLocalSigner(secret, definition.Address, definition.Chains)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		message, err := api.RegistrationMessage(ctx)
		if err != nil {
			return err
		}
		signature, err := signer.SignText(message)
		if err != nil {
			return err
		}
		if err = api.Register(ctx, message, signature, definition.Address.Hex(), "eip155:"+strconv.FormatUint(definition.Chains[0], 10)); err != nil {
			return err
		}
	}
	contracts, err := api.SupportedContracts(ctx)
	if err != nil {
		return err
	}
	add := func(list []lifi.Contract, v lifi.Contract) []lifi.Contract {
		for _, old := range list {
			if old.Chain == v.Chain && strings.EqualFold(old.Address, v.Address) {
				return list
			}
		}
		return append(list, v)
	}
	for _, route := range c.Routes {
		contracts.Input = add(contracts.Input, lifi.Contract{Chain: "eip155:" + strconv.FormatUint(route.OriginChain, 10), Address: route.InputSettler.Hex()})
		contracts.Output = add(contracts.Output, lifi.Contract{Chain: "eip155:" + strconv.FormatUint(route.DestinationChain, 10), Address: route.OutputSettler.Hex()})
	}
	if err = api.SetSupportedContracts(ctx, contracts); err != nil {
		return err
	}
	after, err := api.SupportedContracts(ctx)
	if err != nil {
		return err
	}
	for _, expected := range append(contracts.Input, contracts.Output...) {
		found := false
		for _, actual := range append(after.Input, after.Output...) {
			found = found || expected.Chain == actual.Chain && strings.EqualFold(expected.Address, actual.Address)
		}
		if !found {
			return errors.New("supported-contract registration did not persist")
		}
	}
	identities, err = api.Identities(ctx)
	if err != nil {
		return err
	}
	for _, definition := range c.Signers {
		found := false
		for _, id := range identities {
			found = found || strings.EqualFold(id, definition.Address.Hex())
		}
		if !found {
			return errors.New("solver identity registration did not persist")
		}
	}
	fmt.Fprintln(os.Stdout, "Solver identities and supported contracts verified")
	return nil
}

// publishOnly keeps the development quote available before a user creates the
// short-lived order. It never loads a signing key or starts execution workers.
func publishOnly(ctx context.Context, c config.Config) error {
	check, cancel := context.WithTimeout(ctx, 2*time.Minute)
	_, err := preflight.Run(check, c)
	cancel()
	if err != nil {
		return err
	}
	key, err := config.Secret(c.APIKeyEnv)
	if err != nil {
		return err
	}
	api, err := lifi.New(c.OrderAPI, key, c.RequestsPerSecond)
	if err != nil {
		return err
	}
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
			return errors.New("register the configured solver before publishing")
		}
	}
	clients := map[uint64]*ethclient.Client{}
	defer func() {
		for _, client := range clients {
			client.Close()
		}
	}()
	for _, chain := range c.Chains {
		endpoint, err := chain.URL()
		if err != nil {
			return err
		}
		client, err := evm.Dial(ctx, endpoint, chain.ID, c.RequestsPerSecond)
		if err != nil {
			return err
		}
		clients[chain.ID] = client
	}
	planner := solver.Service{Engine: &solver.Engine{Config: c}, API: api}
	defer func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		for _, route := range c.Routes {
			quote, err := planner.Quote(route, true)
			if err == nil {
				err = api.Withdraw(shutdown, quote)
			}
			if err != nil {
				fmt.Fprintln(os.Stderr, "Quote withdrawal failed; quote expiry remains the fallback")
			}
		}
	}()
	for {
		for _, route := range c.Routes {
			var address common.Address
			for _, definition := range c.Signers {
				if definition.Name == route.Signer {
					address = definition.Address
				}
			}
			balance, err := evm.Balance(ctx, clients[route.DestinationChain], route.OutputToken, address)
			if err != nil {
				return err
			}
			cap, _ := evm.Uint(route.MaxOutput, 256)
			if balance.Cmp(cap) < 0 {
				return errors.New("insufficient destination inventory to publish")
			}
			quote, err := planner.Quote(route, false)
			if err != nil {
				return err
			}
			if err = api.Publish(ctx, quote); err != nil {
				return err
			}
			if err = api.VerifyQuote(ctx, quote); err != nil {
				return err
			}
			if err = json.NewEncoder(os.Stdout).Encode(struct {
				State  string `json:"state"`
				Route  string `json:"route"`
				Expiry int64  `json:"expiry"`
			}{"quote-ready", route.Name, quote.Expiry}); err != nil {
				return err
			}
		}
		timer := time.NewTimer(15 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func checkProofAccess(ctx context.Context, c config.Config, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	audit, err := preflight.AuditOrder(ctx, c, id)
	if err != nil {
		return err
	}
	key, err := config.Secret(c.PolymerKeyEnv)
	if err != nil {
		return err
	}
	client, err := polymer.New(c.PolymerAPI, key, c.PolymerRequest, c.PolymerQuery, c.RequestsPerSecond)
	if err != nil {
		return err
	}
	job, err := client.Request(ctx, polymer.Log{ChainID: audit.DestinationChain, BlockNumber: audit.FillBlock, Index: audit.GlobalLogIndex})
	if err != nil {
		return err
	}
	for {
		proof, err := client.Query(ctx, job)
		if err == nil {
			return json.NewEncoder(os.Stdout).Encode(struct {
				Job    uint64 `json:"job_id"`
				Size   int    `json:"proof_bytes"`
				Status string `json:"status"`
			}{job, len(proof), "complete"})
		}
		if !errors.Is(err, polymer.ErrPending) {
			return err
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
