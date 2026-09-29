// Package solver coordinates event sources and durable, protocol-independent execution.
package solver

import (
	"context"
	"encoding/json"
	"errors"
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
}
type Service struct {
	Quotes     QuotePublisher
	Engine     *Engine
	Log        *zap.Logger
	Shutdown   func()
	Node       string
	Sources    []intent.Source
	Interval   time.Duration
	Workers    int
	Discovered atomic.Uint64
	Advanced   atomic.Uint64
	Failures   atomic.Uint64
	Publish    bool
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
	delay := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		err := source.Run(ctx, s.Accept)
		if ctx.Err() != nil {
			return
		}
		s.Failures.Add(1)
		s.Log.Warn("intent source disconnected", zap.Error(err))
		if time.Since(started) > time.Minute {
			delay = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		delay = min(delay*2, 30*time.Second)
	}
}

func (s *Service) Run(ctx context.Context) error {
	if s.Publish && s.Quotes == nil {
		return errors.New("quote publication requires a configured publisher")
	}
	var wg sync.WaitGroup
	for _, source := range s.Sources {
		wg.Go(func() { s.runSource(ctx, source) })
	}
	for worker := 0; worker < s.Workers; worker++ {
		wg.Go(func() {
			s.loop(ctx, "execution", s.Interval, func(ctx context.Context) error { return s.work(ctx, worker) })
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
		s.loop(ctx, "recovery", 30*time.Second, s.Engine.Recover)
	})
	<-ctx.Done()
	wg.Wait()
	return nil
}

func (s *Service) loop(ctx context.Context, name string, interval time.Duration, action func(context.Context) error) {
	delay := interval
	for ctx.Err() == nil {
		step, cancel := context.WithTimeout(ctx, 90*time.Second)
		err := action(step)
		cancel()
		if err != nil && !errors.Is(err, context.Canceled) {
			s.Failures.Add(1)
			s.Log.Warn("cycle failed", zap.String("cycle", name), zap.Error(err))
			delay = min(delay*2, time.Minute)
			delay = max(delay, intent.RetryDelay(err))
		} else {
			delay = interval
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
	if !control.Allows(s.Node, worker, s.Workers) {
		return nil
	}
	const pageSize int64 = 100
	for offset := int64(0); ctx.Err() == nil; offset += pageSize {
		ids, err := s.Engine.Store.Ready(ctx, pageSize, offset)
		if err != nil {
			return err
		}
		for _, id := range ids {
			lease, err := s.Engine.Store.Acquire(ctx, coordination.IntentResource(id), 60*time.Second)
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
				s.Log.Warn("intent deferred", zap.String("intent_id", id), zap.Error(err))
			}
			return nil
		}
		if int64(len(ids)) < pageSize {
			return nil
		}
	}
	return ctx.Err()
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
	return err
}
