// Package solver coordinates event sources and durable, protocol-independent execution.
package solver

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"go.uber.org/zap"
)

type QuotePublisher interface {
	Refresh(context.Context, bool) error
	Run(context.Context, func(context.Context) (bool, error), func(error))
	ActiveBindings() int64
}
type Service struct {
	Quotes           QuotePublisher
	Engine           *Engine
	Log              *zap.Logger
	Shutdown         func()
	Node             string
	Sources          []intent.Source
	Interval         time.Duration
	Workers          int
	Discovered       atomic.Uint64
	Advanced         atomic.Uint64
	Failures         atomic.Uint64
	SourceOwners     atomic.Int64
	SourceReconnects atomic.Uint64
	running          atomic.Bool
	Publish          bool
	Execute          bool
}

func (s *Service) Close() {
	if s.Shutdown != nil {
		s.Shutdown()
	}
}

func (s *Service) PublishQuotes(ctx context.Context, withdraw bool) error {
	if s.Quotes == nil {
		return errors.New("quote publisher unavailable")
	}
	return s.Quotes.Refresh(ctx, withdraw)
}

func (s *Service) Accept(ctx context.Context, candidate intent.Candidate) error {
	prepared, err := s.Engine.Prepare(candidate)
	if errors.Is(err, intent.ErrRejected) {
		return nil
	}
	if err != nil {
		return err
	}
	data, err := json.Marshal(prepared)
	if err != nil {
		return err
	}
	key := prepared.Identity().Key()
	added, err := s.Engine.Store.Enqueue(ctx, key, string(data))
	if err == nil && added {
		s.Discovered.Add(1)
		s.Log.Info("intent discovered", zap.String("intent_id", key))
	}
	return err
}

func (s *Service) runSource(ctx context.Context, source intent.Source) {
	for ctx.Err() == nil {
		err := coordination.RunOwned(ctx, s.Engine.Store, coordination.SourceLease(source.Identity()), 30*time.Second, func(owned context.Context, _ coordination.Lease) error {
			s.SourceOwners.Add(1)
			defer s.SourceOwners.Add(-1)
			s.reconnectSource(owned, source)
			return owned.Err()
		})
		if ctx.Err() != nil {
			return
		}
		if !errors.Is(err, coordination.ErrBusy) {
			s.Failures.Add(1)
			s.Log.Warn("source ownership lost", zap.Error(err))
		}
		if !wait(ctx, retryDelay(time.Second, err)) {
			return
		}
	}
}

func (s *Service) reconnectSource(ctx context.Context, source intent.Source) {
	delay := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := source.Run(ctx, s.Accept)
		if ctx.Err() != nil {
			return
		}
		s.Failures.Add(1)
		s.SourceReconnects.Add(1)
		s.Log.Warn("intent source disconnected", zap.Error(err))
		if time.Since(started) > time.Minute {
			delay = time.Second
		}
		if !wait(ctx, retryDelay(delay, err)) {
			return
		}
		delay = min(delay*2, 30*time.Second)
	}
}

func retryDelay(backoff time.Duration, err error) time.Duration {
	base := max(backoff, intent.RetryDelay(err))
	// Positive jitter must never shorten a provider's Retry-After deadline.
	jitter, randomErr := rand.Int(rand.Reader, big.NewInt(int64(min(base/4, time.Second))+1))
	if randomErr != nil {
		return base
	}
	return base + time.Duration(jitter.Int64())
}

func wait(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Service) ValidateRun() error {
	if s.Publish && s.Quotes == nil {
		return errors.New("quote publication requires a configured publisher")
	}
	if s.Publish && !s.Execute {
		return errors.New("quote publication requires execution")
	}
	return nil
}

func (s *Service) Running() bool { return s.running.Load() }

func (s *Service) Run(ctx context.Context) error {
	if err := s.ValidateRun(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.running.Store(true)
	defer s.running.Store(false)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	unsafe := make(chan error, 1)
	var wg sync.WaitGroup
	for _, source := range s.Sources {
		wg.Go(func() { s.runSource(ctx, source) })
	}
	if s.Execute {
		for worker := 0; worker < s.Workers; worker++ {
			wg.Go(func() {
				s.loop(ctx, "execution", s.Interval, func(ctx context.Context) (bool, error) { return s.work(ctx, worker) }, func(ctx context.Context, delay time.Duration) error {
					control, err := s.Engine.Store.Control(ctx)
					if err != nil {
						return err
					}
					if !control.Allows(s.Node, worker, s.Workers) {
						wait(ctx, delay)
						return ctx.Err()
					}
					return s.Engine.Store.Wait(ctx, delay)
				})
			})
		}
		wg.Go(func() {
			s.loop(ctx, "recovery", 30*time.Second, func(ctx context.Context) (bool, error) { return false, s.Engine.Recover(ctx) }, nil)
		})
	}
	if s.Publish {
		wg.Go(func() {
			s.Quotes.Run(ctx, func(ctx context.Context) (bool, error) {
				control, err := s.Engine.Store.Control(ctx)
				return control.Paused, err
			}, func(err error) {
				s.Failures.Add(1)
				s.Log.Warn("quote refresh failed", zap.Error(err))
			})
		})
	}
	wg.Go(func() {
		for ctx.Err() == nil {
			check, stop := context.WithTimeout(ctx, 5*time.Second)
			err := s.Engine.Store.Ping(check)
			stop()
			if errors.Is(err, coordination.ErrUnsafeStorage) {
				unsafe <- err
				return
			}
			if !wait(ctx, time.Second) {
				return
			}
		}
	})
	var err error
	select {
	case <-ctx.Done():
	case err = <-unsafe:
	}
	cancel()
	s.running.Store(false)
	wg.Wait()
	return err
}

func (s *Service) loop(ctx context.Context, name string, interval time.Duration, action func(context.Context) (bool, error), idle func(context.Context, time.Duration) error) {
	delay := interval
	for ctx.Err() == nil {
		step, cancel := context.WithTimeout(ctx, 90*time.Second)
		worked, err := action(step)
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) {
			s.Failures.Add(1)
			s.Log.Warn("cycle failed", zap.String("cycle", name), zap.Error(err))
			delay = min(delay*2, time.Minute)
			delay = max(delay, intent.RetryDelay(err))
		} else {
			delay = interval
			if worked {
				continue
			}
		}
		if err == nil && idle != nil {
			if err = idle(ctx, delay); err == nil {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			s.Failures.Add(1)
			s.Log.Warn("queue wait failed", zap.Error(err))
		}
		wait(ctx, delay)
	}
}

func (s *Service) work(ctx context.Context, worker int) (bool, error) {
	if !s.Execute {
		return false, nil
	}
	control, err := s.Engine.Store.Control(ctx)
	if err != nil {
		return false, err
	}
	if !control.Allows(s.Node, worker, s.Workers) {
		return false, nil
	}
	claim, err := s.Engine.Store.ClaimNext(ctx, 60*time.Second)
	if errors.Is(err, coordination.ErrNotReady) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	err = s.process(ctx, claim.Lease, claim.ID)
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	_ = s.Engine.Store.Release(releaseCtx, claim.Lease)
	cancel()
	return err == nil, err
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
		s.Log.Info("intent advanced", zap.String("intent_id", id), zap.String("from_stage", string(record.Stage)))
		return nil
	}
	var progress intent.Progress
	if record.Detail != "" {
		if json.Unmarshal([]byte(record.Detail), &progress) != nil {
			return errors.New("corrupt intent progress")
		}
	}
	progress.LastError = err.Error()
	if errors.Is(err, intent.ErrRejected) {
		b, _ := json.Marshal(progress)
		s.Log.Info("intent rejected", zap.String("intent_id", id), zap.Error(err))
		return s.Engine.Store.Advance(ctx, lease, id, record.Stage, intent.Rejected, string(b), true, 0)
	}
	var deferred *intent.Deferred
	var delay time.Duration
	if errors.As(err, &deferred) {
		delay = max(deferred.After, time.Second)
	} else {
		progress.Attempts = min(progress.Attempts+1, 10)
		delay = time.Second * time.Duration(1<<progress.Attempts)
	}
	if errors.Is(err, intent.ErrObserve) {
		delay = time.Minute
	}
	b, _ := json.Marshal(progress)
	delay = min(delay, 5*time.Minute)
	delay = max(delay, intent.RetryDelay(err))
	if updateErr := s.Engine.Store.Advance(ctx, lease, id, record.Stage, record.Stage, string(b), false, delay); updateErr != nil {
		return updateErr
	}
	if !errors.Is(err, coordination.ErrLeaseLost) {
		s.Log.Warn("intent deferred", zap.String("intent_id", id), zap.Error(err))
	}
	return nil
}
