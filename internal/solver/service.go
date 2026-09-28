package solver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/lifi"
	"github.com/LuisUrrutia/goif-solver/internal/polymer"
	"github.com/LuisUrrutia/goif-solver/internal/transport"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

type OrderSource interface {
	Orders(context.Context, url.Values, int) (lifi.Page, error)
}
type Service struct {
	Sources    []OrderSource
	Publish    bool
	Engine     *Engine
	API        *lifi.Client
	Log        *zap.Logger
	Node       string
	Discovered atomic.Uint64
	Advanced   atomic.Uint64
	Failures   atomic.Uint64
	redis      *redis.Client
}

func New(ctx context.Context, c config.Config, node string, execute bool, log *zap.Logger) (*Service, error) {
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
	engine := &Engine{Config: c, Store: store, Execute: execute, Clients: map[uint64]*ethclient.Client{}, Senders: map[string]map[uint64]*evm.Sender{}}
	service := &Service{Engine: engine, Log: log, Node: node, redis: client}
	ok := false
	defer func() {
		if !ok {
			service.Close()
		}
	}()
	if err = store.Ping(ctx); err != nil {
		return nil, errors.New("Redis unavailable")
	}
	// Listen addresses are local. The rest of the public policy must match fleet-wide.
	policy := c
	policy.Listen = ""
	b, _ := json.Marshal(policy)
	digest := sha256.Sum256(b)
	if err = store.BindConfig(ctx, hex.EncodeToString(digest[:])); err != nil {
		return nil, errors.New("fleet configuration differs; drain and migrate namespace")
	}
	apiKey := os.Getenv(c.APIKeyEnv)
	if execute && apiKey == "" {
		return nil, fmt.Errorf("required environment variable %s is unset", c.APIKeyEnv)
	}
	service.API, err = lifi.New(c.OrderAPI, apiKey, c.RequestsPerSecond)
	if err != nil {
		return nil, err
	}
	service.Sources = []OrderSource{service.API}
	for _, source := range c.OrderSources {
		key := ""
		if source.KeyEnv != "" {
			var err error
			key, err = config.Secret(source.KeyEnv)
			if err != nil {
				return nil, err
			}
		}
		client, err := lifi.New(source.URL, key, c.RequestsPerSecond)
		if err != nil {
			return nil, err
		}
		service.Sources = append(service.Sources, client)
	}
	for _, chain := range c.Chains {
		endpoint, err := chain.URL()
		if err != nil {
			return nil, err
		}
		rpc, err := evm.Dial(ctx, endpoint, chain.ID, c.RequestsPerSecond)
		if err != nil {
			return nil, err
		}
		engine.Clients[chain.ID] = rpc
	}
	if execute {
		identities, err := service.API.Identities(ctx)
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
		contracts, err := service.API.SupportedContracts(ctx)
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
				engine.Senders[signerConfig.Name][chain.ID] = &evm.Sender{Log: log, Client: engine.Clients[chain.ID], Store: store, Signer: signer, Policy: evm.SendPolicy{Chain: chain.ID, Confirmations: chain.Confirmations, MaxGas: chain.MaxGas, MaxFee: cap}}
			}
		}
	}
	ok = true
	return service, nil
}
func (s *Service) Close() {
	for _, c := range s.Engine.Clients {
		c.Close()
	}
	if s.redis != nil {
		s.redis.Close()
	}
}
func (s *Service) Discover(ctx context.Context) error {
	var failures []error
	for _, source := range s.Sources {
		if err := s.discoverSource(ctx, source); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func (s *Service) discoverSource(ctx context.Context, source OrderSource) error {
	for _, route := range s.Engine.Config.Routes {
		var signer config.Signer
		for _, v := range s.Engine.Config.Signers {
			if v.Name == route.Signer {
				signer = v
			}
		}
		for _, status := range []string{"Signed", "Open"} {
			query := url.Values{"status": {status}, "originChainId": {strconv.FormatUint(route.OriginChain, 10)}, "destinationChainId": {strconv.FormatUint(route.DestinationChain, 10)}}
			if len(s.Engine.Config.OrderAllowlist) == 1 {
				query.Set("onChainOrderId", s.Engine.Config.OrderAllowlist[0].Hex())
			}
			for offset := 0; offset <= 1000; offset += 50 {
				page, err := source.Orders(ctx, query, offset)
				if err != nil {
					return err
				}
				for _, envelope := range page.Data {
					validated, err := evm.Validate(envelope, route, signer.Address, time.Now())
					if err != nil {
						continue
					}
					if !s.Engine.Config.AllowsOrder(validated.ID) {
						continue
					}
					envelope = evm.Canonical(validated)
					b, err := json.Marshal(Work{Version: s.Engine.Config.Version, Route: route.Name, Envelope: envelope})
					if err != nil {
						return err
					}
					added, err := s.Engine.Store.Enqueue(ctx, validated.ID.Hex(), string(b))
					if err != nil {
						return err
					}
					if added {
						s.Discovered.Add(1)
						s.Log.Info("order discovered", zap.String("order_id", validated.ID.Hex()), zap.String("route", route.Name))
					}
				}
				if len(page.Data) < 50 || offset+50 >= page.Meta.Total {
					break
				}
				if offset == 1000 {
					return errors.New("order API pagination window exhausted")
				}
			}
		}
	}
	return nil
}
func (s *Service) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Go(func() {
		s.loop(ctx, "discovery", func(ctx context.Context) error {
			control, err := s.Engine.Store.Control(ctx)
			if err != nil {
				return err
			}
			if control.Paused {
				return nil
			}
			return s.Discover(ctx)
		})
	})
	for worker := 0; worker < s.Engine.Config.Workers; worker++ {
		wg.Go(func() { s.loop(ctx, "worker", func(ctx context.Context) error { return s.work(ctx, worker) }) })
	}
	if s.Publish {
		wg.Go(func() {
			s.loop(ctx, "quotes", func(ctx context.Context) error {
				control, err := s.Engine.Store.Control(ctx)
				if err != nil {
					return err
				}
				return s.PublishQuotes(ctx, control.Paused)
			})
		})
	}
	if s.Engine.Execute {
		wg.Go(func() {
			s.loop(ctx, "recovery", func(ctx context.Context) error {
				for _, signers := range s.Engine.Senders {
					for _, sender := range signers {
						err := sender.Recover(ctx)
						if err != nil && !errors.Is(err, coordination.ErrBusy) && !errors.Is(err, evm.ErrPending) {
							return err
						}
					}
				}
				return nil
			})
		})
	}
	<-ctx.Done()
	wg.Wait()
	return nil
}
func (s *Service) loop(ctx context.Context, name string, action func(context.Context) error) {
	delay := time.Duration(s.Engine.Config.PollSeconds) * time.Second
	for ctx.Err() == nil {
		step, cancel := context.WithTimeout(ctx, 90*time.Second)
		err := action(step)
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) {
			s.Failures.Add(1)
			s.Log.Warn("cycle failed", zap.String("cycle", name), zap.Error(err))
			delay = min(delay*2, time.Minute)
			var remote *transport.StatusError
			if errors.As(err, &remote) {
				delay = max(delay, remote.RetryAfter)
			}
		} else {
			delay = time.Duration(s.Engine.Config.PollSeconds) * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
func (s *Service) work(ctx context.Context, worker int) error {
	control, err := s.Engine.Store.Control(ctx)
	if err != nil {
		return err
	}
	if !control.Allows(s.Node, worker, s.Engine.Config.Workers) {
		return nil
	}
	ids, err := s.Engine.Store.Ready(ctx, 100)
	if err != nil {
		return err
	}
	for _, id := range ids {
		lease, err := s.Engine.Store.Acquire(ctx, "order:"+id, 60*time.Second)
		if errors.Is(err, coordination.ErrBusy) {
			continue
		}
		if err != nil {
			return err
		}
		err = s.process(ctx, lease, id)
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		_ = s.Engine.Store.Release(releaseCtx, lease)
		cancel()
		if err != nil && !errors.Is(err, coordination.ErrLeaseLost) {
			s.Log.Warn("order deferred", zap.String("order_id", id), zap.Error(err))
		}
		return nil
	}
	return nil
}
func (s *Service) process(ctx context.Context, lease coordination.Lease, id string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if s.Engine.Store.Renew(ctx, lease, 60*time.Second) != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-done }()
	record, err := s.Engine.Store.Record(ctx, id)
	if err != nil {
		return err
	}
	err = s.Engine.Step(ctx, lease, record)
	if err == nil {
		s.Advanced.Add(1)
		s.Log.Info("order advanced", zap.String("order_id", id), zap.String("from_stage", record.Stage))
		return nil
	}
	var progress Progress
	if record.Detail != "" {
		if json.Unmarshal([]byte(record.Detail), &progress) != nil {
			return errors.New("corrupt order progress")
		}
	}
	progress.LastError = err.Error()
	if errors.Is(err, ErrRejected) {
		b, _ := json.Marshal(progress)
		s.Log.Info("order rejected", zap.String("order_id", id), zap.Error(err))
		return s.Engine.Store.Advance(ctx, lease, id, record.Stage, "rejected", string(b), true, 0)
	}
	progress.Attempts = min(progress.Attempts+1, 10)
	delay := time.Second * time.Duration(1<<progress.Attempts)
	if errors.Is(err, ErrObserve) {
		delay = time.Minute
	}
	b, _ := json.Marshal(progress)
	delay = min(delay, 5*time.Minute)
	var remote *transport.StatusError
	if errors.As(err, &remote) {
		delay = max(delay, remote.RetryAfter)
	}
	if updateErr := s.Engine.Store.Advance(ctx, lease, id, record.Stage, record.Stage, string(b), false, delay); updateErr != nil {
		return updateErr
	}
	return err
}
