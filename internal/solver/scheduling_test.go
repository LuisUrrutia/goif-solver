package solver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/storage/memorystore"
	"go.uber.org/zap"
)

const backlogKind intent.Kind = "backlog"

type backlogExecutor struct {
	*independentExecutor
	step func(context.Context, coordination.Lease, coordination.Record) error
}

func (e *backlogExecutor) Step(ctx context.Context, lease coordination.Lease, record coordination.Record) error {
	return e.step(ctx, lease, record)
}

func backlogService(t *testing.T, store coordination.Backend, executor Executor, workers int) *Service {
	t.Helper()
	service := &Service{Engine: &Engine{Store: store, Executors: map[intent.Kind]Executor{backlogKind: executor}}, Log: zap.NewNop(), Workers: workers, Interval: 5 * time.Second, Execute: true}
	for i := range 20 {
		if err := service.Accept(t.Context(), intent.Candidate{Kind: backlogKind, ID: fmt.Sprintf("%02d", i), Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
	}
	return service
}

func runBacklog(t *testing.T, service *Service) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	return func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}

func TestWorkersDrainReadyBacklogWithoutIntervalDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := memorystore.New()
		service := backlogService(t, store, &independentExecutor{store: store}, 2)

		stop := runBacklog(t, service)
		defer stop()
		time.Sleep(time.Second)
		synctest.Wait()

		stats, err := store.Stats(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if service.Advanced.Load() != 20 || stats.Due != 0 || stats.Outstanding != 0 {
			t.Fatalf("ready backlog still paced by idle interval: advanced=%d stats=%+v", service.Advanced.Load(), stats)
		}
		t.Log("20 immediately completable intents finished within the first second with two workers and a 5s idle interval")
	})
}

func TestIdleWorkerWakesOnAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := memorystore.New()
		service := &Service{Engine: &Engine{Store: store, Executors: map[intent.Kind]Executor{backlogKind: &independentExecutor{store: store}}}, Log: zap.NewNop(), Workers: 2, Interval: time.Minute, Execute: true}
		stop := runBacklog(t, service)
		defer stop()
		synctest.Wait()
		time.Sleep(100 * time.Millisecond)

		if err := service.Accept(t.Context(), intent.Candidate{Kind: backlogKind, ID: "new", Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()

		if service.Advanced.Load() != 1 {
			t.Fatal("admission waited for the idle timer", service.Advanced.Load())
		}
	})
}

func TestIdleWorkerWakesAtRetryDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := memorystore.New()
		executor := &backlogExecutor{independentExecutor: &independentExecutor{store: store}}
		var attempts atomic.Int64
		executor.step = func(ctx context.Context, lease coordination.Lease, record coordination.Record) error {
			if attempts.Add(1) == 1 {
				return &intent.Deferred{After: 2 * time.Second, Cause: errors.New("pending")}
			}
			return executor.independentExecutor.Step(ctx, lease, record)
		}
		service := &Service{Engine: &Engine{Store: store, Executors: map[intent.Kind]Executor{backlogKind: executor}}, Log: zap.NewNop(), Workers: 1, Interval: time.Minute, Execute: true}
		if err := service.Accept(t.Context(), intent.Candidate{Kind: backlogKind, ID: "retry", Payload: json.RawMessage(`{}`)}); err != nil {
			t.Fatal(err)
		}
		stop := runBacklog(t, service)
		defer stop()
		synctest.Wait()

		time.Sleep(2 * time.Second)
		synctest.Wait()

		if attempts.Load() != 2 || service.Advanced.Load() != 1 {
			t.Fatal("retry waited for the idle interval", attempts.Load(), service.Advanced.Load())
		}
	})
}

func TestWorkersDrainAroundDeferredIntents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := memorystore.New()
		executor := &backlogExecutor{independentExecutor: &independentExecutor{store: store}}
		var attempts atomic.Int64
		executor.step = func(ctx context.Context, lease coordination.Lease, record coordination.Record) error {
			if record.ID == "backlog/00" {
				attempts.Add(1)
				return &intent.Deferred{After: 30 * time.Second, Cause: errors.New("awaiting finality")}
			}
			return executor.independentExecutor.Step(ctx, lease, record)
		}
		service := backlogService(t, store, executor, 1)

		stop := runBacklog(t, service)
		defer stop()
		synctest.Wait()
		time.Sleep(29 * time.Second)
		synctest.Wait()

		if attempts.Load() != 1 || service.Advanced.Load() != 19 || service.Failures.Load() != 0 {
			t.Fatalf("deferral blocked other work or retried early: attempts=%d advanced=%d failures=%d", attempts.Load(), service.Advanced.Load(), service.Failures.Load())
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if attempts.Load() != 2 {
			t.Fatal("deferred work did not become runnable", attempts.Load())
		}
	})
}

func TestWorkersCheckPauseBetweenReadySteps(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := memorystore.New()
		executor := &backlogExecutor{independentExecutor: &independentExecutor{store: store}}
		var steps atomic.Int64
		executor.step = func(ctx context.Context, lease coordination.Lease, record coordination.Record) error {
			if steps.Add(1) == 1 {
				if err := store.SetControl(ctx, 0, coordination.Control{Version: 1, Paused: true}); err != nil {
					return err
				}
			}
			return executor.independentExecutor.Step(ctx, lease, record)
		}
		service := backlogService(t, store, executor, 1)

		stop := runBacklog(t, service)
		defer stop()
		synctest.Wait()

		if steps.Load() != 1 {
			t.Fatal("worker ignored pause while draining", steps.Load())
		}
		if err := store.SetControl(t.Context(), 1, coordination.Control{Version: 2}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if steps.Load() != 20 {
			t.Fatal("worker did not resume ready backlog", steps.Load())
		}
	})
}

type unavailableTransitions struct{ *memorystore.Store }

func (*unavailableTransitions) Advance(context.Context, coordination.Lease, string, intent.Stage, intent.Stage, string, bool, time.Duration) error {
	return errors.New("durable transition unavailable")
}

func TestWorkersBackOffWhenProgressCannotBePersisted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &unavailableTransitions{Store: memorystore.New()}
		executor := &backlogExecutor{independentExecutor: &independentExecutor{store: store}}
		var attempts atomic.Int64
		executor.step = func(ctx context.Context, lease coordination.Lease, record coordination.Record) error {
			attempts.Add(1)
			return executor.independentExecutor.Step(ctx, lease, record)
		}
		service := backlogService(t, store, executor, 1)

		stop := runBacklog(t, service)
		defer stop()
		time.Sleep(time.Second)
		synctest.Wait()

		if attempts.Load() != 1 || service.Failures.Load() != 1 {
			t.Fatalf("storage failure caused a busy retry: attempts=%d failures=%d", attempts.Load(), service.Failures.Load())
		}
		time.Sleep(10 * time.Second)
		synctest.Wait()
		if attempts.Load() != 2 || service.Failures.Load() != 2 {
			t.Fatalf("storage retry did not honor backoff: attempts=%d failures=%d", attempts.Load(), service.Failures.Load())
		}
		stats, err := store.Stats(t.Context())
		if err != nil || stats.Outstanding != 20 {
			t.Fatal("lost work during storage failure", stats, err)
		}
	})
}
