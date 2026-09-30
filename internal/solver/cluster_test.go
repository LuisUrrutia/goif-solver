package solver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/quote"
	"github.com/LuisUrrutia/goif-solver/internal/storage/memorystore"
	"github.com/LuisUrrutia/goif-solver/internal/storage/redisstore"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func clusterStores(t *testing.T) (*redisstore.Store, *redisstore.Store) {
	t.Helper()
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("run scripts/check.sh for real Redis tests")
	}
	namespace := fmt.Sprintf("cluster-%d", time.Now().UnixNano())
	makeStore := func() *redisstore.Store {
		client := redis.NewClient(&redis.Options{Addr: addr})
		t.Cleanup(func() { _ = client.Close() })
		s, err := redisstore.New(client, namespace)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	return makeStore(), makeStore()
}

type unsafeStore struct{ *memorystore.Store }

func (*unsafeStore) Ping(context.Context) error { return coordination.ErrUnsafeStorage }

func TestServiceStopsWhenStorageSafetyChanges(t *testing.T) {
	service := Service{Engine: &Engine{Store: &unsafeStore{memorystore.New()}}, Log: zap.NewNop()}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	err := service.Run(ctx)

	if !errors.Is(err, coordination.ErrUnsafeStorage) || service.Running() {
		t.Fatal("unsafe storage left engine running", err)
	}
}

func TestClusterSourceHasOneOwnerAndTransfersOnExit(t *testing.T) {
	a, b := clusterStores(t)
	var effects, active atomic.Int32
	entered := make(chan struct{}, 2)
	source := sourceFunc(func(ctx context.Context, _ intent.Emit) error {
		if active.Add(1) != 1 {
			t.Error("replicas opened the same subscription concurrently")
		}
		defer active.Add(-1)
		entered <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	})
	first, stopFirst := context.WithCancel(t.Context())
	second, stopSecond := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	defer func() { stopFirst(); stopSecond(); wg.Wait() }()
	wg.Go(func() { clusterService(a, &effects).runSource(first, source) })
	awaitSignal(t, entered)
	wg.Go(func() { clusterService(b, &effects).runSource(second, source) })

	stopFirst()
	awaitSignal(t, entered)
	stopSecond()
	wg.Wait()
	if active.Load() != 0 {
		t.Fatal("source survived cancellation")
	}
}

func awaitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for replica")
	}
}

type quotePublisherFunc func(context.Context, quote.Offer) error

func (f quotePublisherFunc) PublishOffer(ctx context.Context, offer quote.Offer) error {
	return f(ctx, offer)
}

func TestClusterQuoteOwnerExitPreservesOfferAndTransfersOwnership(t *testing.T) {
	a, b := clusterStores(t)
	offers := make(chan quote.Offer, 16)
	var paused atomic.Bool
	binding := quote.Binding{Name: "shared", Source: quoteSourceFunc(func(_ context.Context, withdraw bool) (quote.Offer, error) {
		offer := quote.Offer{Expiry: time.Now().Add(time.Minute).Unix()}
		if !withdraw {
			offer.Ranges = []quote.PriceRange{{}}
		}
		return offer, nil
	}), Publisher: quotePublisherFunc(func(_ context.Context, offer quote.Offer) error { offers <- offer; return nil })}
	first := &Quoter{Store: a, Sources: []quote.Binding{binding}, Enabled: true}
	second := &Quoter{Store: b, Sources: []quote.Binding{binding}, Enabled: true}
	start := func(q *Quoter) context.CancelFunc {
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			q.Run(ctx, func(context.Context) (bool, error) { return paused.Load(), nil }, func(err error) { t.Error(err) })
		}()
		stop := func() { cancel(); <-done }
		t.Cleanup(stop)
		return stop
	}
	nextOffer := func() quote.Offer {
		t.Helper()
		select {
		case offer := <-offers:
			return offer
		case <-time.After(5 * time.Second):
			t.Fatal("publisher did not act")
			return quote.Offer{}
		}
	}
	stopFirst := start(first)
	if len(nextOffer().Ranges) == 0 {
		t.Fatal("first offer was withdrawn")
	}
	stopSecond := start(second)
	if err := second.Refresh(t.Context(), true); !errors.Is(err, coordination.ErrBusy) {
		t.Fatal("non-owner withdrew shared offer", err)
	}

	stopFirst()
	if len(nextOffer().Ranges) == 0 {
		t.Fatal("owner shutdown withdrew survivor quote")
	}
	paused.Store(true)
	if len(nextOffer().Ranges) != 0 {
		t.Fatal("fleet pause did not withdraw offer")
	}
	stopSecond()
	select {
	case <-offers:
		t.Fatal("pod shutdown published another offer")
	default:
	}
}

type clusterExecutor struct {
	store   coordination.Backend
	effects *atomic.Int32
}

func (e *clusterExecutor) Prepare(c intent.Candidate) (intent.Candidate, error) { return c, nil }
func (e *clusterExecutor) Recover(context.Context) error                        { return nil }
func (e *clusterExecutor) Step(ctx context.Context, lease coordination.Lease, r coordination.Record) error {
	if r.Stage == intent.Settled {
		return nil
	}
	e.effects.Add(1)
	return e.store.Advance(ctx, lease, r.ID, r.Stage, intent.Settled, "", true, 0)
}

func clusterService(s *redisstore.Store, effects *atomic.Int32) *Service {
	return &Service{Execute: true, Node: "cluster", Workers: 1, Log: zap.NewNop(), Engine: &Engine{Store: s, Executors: map[intent.Kind]Executor{"cluster": &clusterExecutor{store: s, effects: effects}}}}
}

func clusterCandidate(id string) intent.Candidate {
	return intent.Candidate{ID: id, Kind: "cluster", Payload: json.RawMessage("{}")}
}

type waitingStore struct {
	coordination.Backend
	waiting chan struct{}
}

func (s *waitingStore) Wait(ctx context.Context, interval time.Duration) error {
	select {
	case s.waiting <- struct{}{}:
	default:
	}
	return s.Backend.Wait(ctx, interval)
}

func TestClusterIdleReplicaWakesOnRemoteAdmission(t *testing.T) {
	a, b := clusterStores(t)
	var effects atomic.Int32
	producer, consumer := clusterService(a, &effects), clusterService(b, &effects)
	consumer.Interval = time.Minute
	waiting := &waitingStore{Backend: b, waiting: make(chan struct{}, 1)}
	consumer.Engine.Store = waiting
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	done := make(chan error, 1)
	go func() { done <- consumer.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	awaitSignal(t, waiting.waiting)

	if err := producer.Accept(ctx, clusterCandidate("remote")); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for effects.Load() == 0 {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("remote admission waited for the polling interval")
		}
	}
}

func TestClusterReplicaIntakeDeduplicatesBeforeExecution(t *testing.T) {
	a, b := clusterStores(t)
	var effects atomic.Int32
	nodes := []*Service{clusterService(a, &effects), clusterService(b, &effects)}
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Go(func() {
			if err := nodes[i%2].Accept(t.Context(), clusterCandidate("shared")); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n := nodes[0].Discovered.Load() + nodes[1].Discovered.Load(); n != 1 {
		t.Fatalf("discoveries=%d", n)
	}
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			if _, err := nodes[i%2].work(t.Context(), 0); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	r, err := a.Record(t.Context(), "cluster/shared")
	if err != nil || r.Stage != intent.Settled || effects.Load() != 1 {
		t.Fatalf("record=%+v effects=%d err=%v", r, effects.Load(), err)
	}
	t.Log("64 deliveries across two Redis clients produced one record and one terminal execution")
}

func TestClusterLeasedHeadDoesNotHideRunnableQueueTail(t *testing.T) {
	a, b := clusterStores(t)
	var effects atomic.Int32
	service := clusterService(a, &effects)
	for i := 0; i < 251; i++ {
		id := fmt.Sprintf("%03d", i)
		if err := service.Accept(t.Context(), clusterCandidate(id)); err != nil {
			t.Fatal(err)
		}
		if i < 250 {
			if _, err := b.Acquire(t.Context(), coordination.IntentResource("cluster/"+id), time.Minute); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := service.work(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	if effects.Load() != 1 {
		t.Fatal("leased pages hid the runnable tail")
	}
	record, err := b.Record(t.Context(), "cluster/250")
	if err != nil || record.Stage != intent.Settled {
		t.Fatalf("tail was not settled: %+v %v", record, err)
	}
}

func TestClusterObserverCannotDeferExecutingReplica(t *testing.T) {
	a, b := clusterStores(t)
	var effects atomic.Int32
	observer, executor := clusterService(a, &effects), clusterService(b, &effects)
	observer.Execute = false
	if err := observer.Accept(t.Context(), clusterCandidate("mixed-role")); err != nil {
		t.Fatal(err)
	}
	before, err := a.Record(t.Context(), "cluster/mixed-role")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := observer.work(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	after, err := a.Record(t.Context(), before.ID)
	if err != nil || before != after || effects.Load() != 0 {
		t.Fatal("observer changed shared work", err)
	}
	if _, err := executor.work(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	if effects.Load() != 1 {
		t.Fatal("observer delayed executing replica")
	}
}

func TestClusterWorkersDrainBurstOnce(t *testing.T) {
	a, b := clusterStores(t)
	var effects atomic.Int32
	nodes := []*Service{clusterService(a, &effects), clusterService(b, &effects)}
	var intake sync.WaitGroup
	for i := range 80 {
		intake.Go(func() {
			if err := nodes[i%2].Accept(t.Context(), clusterCandidate(fmt.Sprintf("burst-%02d", i/2))); err != nil {
				t.Error(err)
			}
		})
	}
	intake.Wait()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	done := make(chan error, len(nodes))
	for i, node := range nodes {
		node.Node = fmt.Sprintf("burst-%d", i)
		node.Workers = 2
		node.Interval = time.Minute
		go func() { done <- node.Run(ctx) }()
	}
	defer func() {
		cancel()
		for range nodes {
			if err := <-done; err != nil {
				t.Error(err)
			}
		}
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		stats, err := a.Stats(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if stats.Outstanding == 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("workers did not drain ready backlog", stats)
		case <-ticker.C:
		}
	}
	cancel()
	if effects.Load() != 40 {
		t.Fatal("duplicate terminal execution", effects.Load())
	}
	if nodes[0].Discovered.Load()+nodes[1].Discovered.Load() != 40 {
		t.Fatal("duplicate intake")
	}
	t.Log("80 deliveries across two Redis clients drained as 40 terminal executions with four workers")
}
