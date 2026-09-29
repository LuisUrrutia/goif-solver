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
	"strings"
	"syscall"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/app"
	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/preflight"
	"github.com/LuisUrrutia/goif-solver/internal/quote"
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
		return errors.New("usage: goif {preflight|proof-check|run|register|publish|withdraw|status|control} -config config/testnet.json")
	}
	command := os.Args[1]
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	path := flags.String("config", "config/testnet.json", "public configuration file")
	node := flags.String("node", os.Getenv("HOSTNAME"), "unique node ID")
	execute := flags.Bool("execute", false, "authorize configured intent execution with injected keys")
	publish := flags.Bool("publish-quotes", false, "publish and renew configured testnet inventory quotes")
	authorizeRegistration := flags.Bool("authorize-registration", false, "authorize identity signatures and supported-contract registration")
	order := flags.String("intent", "", "canonical protocol/native-id for execution or inspection")
	historyProvider := flags.String("history-provider", "", "explicit external provider for historical inspection")
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
		report, err := app.Preflight(ctx, c)
		if err != nil {
			return err
		}
		if *order != "" {
			audit, err := app.AuditIntent(ctx, c, *order, *historyProvider, false)
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(struct {
				Preflight preflight.Report       `json:"preflight"`
				Intent    preflight.IntentReport `json:"intent"`
			}{report, audit})
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	if command == "proof-check" {
		report, err := app.AuditIntent(ctx, c, *order, *historyProvider, true)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	if command == "status" || command == "control" {
		return storedCommand(ctx, c, command, *order, *controlPath)
	}
	if command == "register" {
		if !*authorizeRegistration {
			return errors.New("registration requires -authorize-registration")
		}
		return app.RegisterProviders(ctx, c)
	}
	if command == "publish" {
		if !*publish {
			return errors.New("publication requires -publish-quotes")
		}
		return publication(ctx, c, false)
	}
	if command != "run" && command != "withdraw" {
		return errors.New("unknown command")
	}
	if *publish && !*execute {
		return errors.New("quote publication requires -execute")
	}
	if command == "withdraw" {
		return publication(ctx, c, true)
	}
	if *order != "" {
		identity, err := intent.ParseIdentity(*order)
		if err != nil {
			return err
		}
		c.IntentAllowlist = []intent.Identity{identity}
	}

	if *node == "" {
		var err error
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
	service, err := app.New(ctx, c, *node, *execute, log)
	if err != nil {
		return err
	}
	defer service.Close()
	service.Publish = *publish
	token := os.Getenv(c.ControlTokenEnv)
	if !strings.HasPrefix(c.Listen, "127.0.0.1:") && len(token) < 32 {
		return errors.New("non-loopback HTTP requires a control token of at least 32 characters")
	}
	server := &http.Server{Addr: c.Listen, Handler: service.Handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	engineDone := make(chan struct{})
	go func() { defer close(engineDone); _ = service.Run(runCtx) }()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ListenAndServe() }()
	log.Info("solver started", zap.String("node", *node), zap.Bool("execution_enabled", *execute), zap.Bool("publish_quotes", *publish))
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
	if c.Storage.Kind == config.MemoryStorage {
		return errors.New("memory storage belongs to the running process; use its authenticated /control or /intents endpoint")
	}
	store, closeStore, err := app.OpenStore(c)
	if err != nil {
		return err
	}
	defer closeStore()
	if command == "status" {
		if _, err := intent.ParseIdentity(id); err != nil {
			return err
		}
		record, err := store.Record(ctx, id)
		if err != nil {
			return errors.New("intent record unavailable")
		}
		return json.NewEncoder(os.Stdout).Encode(record)
	}
	if path != "" {
		b, err := os.ReadFile(path) // #nosec G304 G703 -- The local operator explicitly selects this control file; it is not a remote request path.
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

func publication(ctx context.Context, c config.Config, withdraw bool) error {
	return app.PublishQuotes(ctx, c, withdraw, func(route string, offer quote.Offer) {
		state := "quote-ready"
		if len(offer.Ranges) == 0 {
			state = "quote-withdrawn"
		}
		if err := json.NewEncoder(os.Stdout).Encode(struct {
			State  string `json:"state"`
			Route  string `json:"route"`
			Expiry int64  `json:"expiry"`
		}{state, route, offer.Expiry}); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}, func(err error) { fmt.Fprintln(os.Stderr, err) })
}
