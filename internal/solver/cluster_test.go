package solver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
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
	return &Service{Node: "cluster", Workers: 1, Log: zap.NewNop(), Engine: &Engine{Store: s, Executors: map[intent.Kind]Executor{"cluster": &clusterExecutor{store: s, effects: effects}}}}
}

func clusterCandidate(id string) intent.Candidate {
	return intent.Candidate{ID: id, Kind: "cluster", Payload: json.RawMessage("{}")}
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
			if err := nodes[i%2].work(t.Context(), 0); err != nil {
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
	if err := service.work(t.Context(), 0); err != nil {
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
