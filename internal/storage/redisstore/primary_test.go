package redisstore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/redis/go-redis/v9"
)

func TestPrimaryGuardBlocksNewAndExistingPodsAfterEndpointReplacement(t *testing.T) {
	first, second := os.Getenv("TEST_REDIS_ADDR"), os.Getenv("TEST_REDIS_GUARD_ADDR")
	identity := os.Getenv("TEST_REDIS_RUN_ID")
	if first == "" || second == "" || identity == "" {
		t.Skip("run scripts/check.sh for isolated Redis primary tests")
	}
	var replaced atomic.Bool
	options := &redis.Options{Addr: first, PoolSize: 1, ConnMaxLifetime: 50 * time.Millisecond, MaxRetries: -1, Dialer: func(ctx context.Context, network, _ string) (net.Conn, error) {
		addr := first
		if replaced.Load() {
			addr = second
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}}
	store, closeStore, err := Open(options, fmt.Sprintf("primary-%d", time.Now().UnixNano()), identity)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore()
	if err := store.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enqueue(t.Context(), "accepted", "payload"); err != nil {
		t.Fatal(err)
	}

	replaced.Store(true)
	time.Sleep(60 * time.Millisecond)
	if _, err := store.Acquire(t.Context(), "replacement", time.Second); !errors.Is(err, coordination.ErrUnsafeStorage) {
		t.Fatal("existing pod accepted replacement primary", err)
	}
	replaced.Store(false)
	if _, err := store.Enqueue(t.Context(), "after-failure", "payload"); !errors.Is(err, coordination.ErrUnsafeStorage) {
		t.Fatal("unsafe client resumed automatically", err)
	}
	fresh, closeFresh, err := Open(&redis.Options{Addr: second, MaxRetries: -1}, "restarted-pod", identity)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFresh()
	if err := fresh.Ping(t.Context()); !errors.Is(err, coordination.ErrUnsafeStorage) {
		t.Fatal("new pod learned replacement identity", err)
	}
}

func TestPrimaryGuardRejectsUnsafeDurabilityAndLatches(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_GUARD_ADDR")
	if addr == "" {
		t.Skip("run scripts/check.sh for isolated Redis primary tests")
	}
	admin := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = admin.Close() })
	info, err := admin.Info(t.Context(), "server").Result()
	if err != nil {
		t.Fatal(err)
	}
	var identity string
	for line := range strings.SplitSeq(info, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "run_id:"); ok {
			identity = value
		}
	}
	store, closeStore, err := Open(&redis.Options{Addr: addr, MaxRetries: -1}, "durability", identity)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStore()
	if err := store.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := admin.ConfigSet(t.Context(), "appendfsync", "everysec").Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.ConfigSet(context.Background(), "appendfsync", "always").Err(); err != nil {
			t.Error(err)
		}
	})

	if err := store.Ping(t.Context()); !errors.Is(err, coordination.ErrUnsafeStorage) {
		t.Fatal("weak persistence was accepted", err)
	}
	if err := admin.ConfigSet(t.Context(), "appendfsync", "always").Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Acquire(t.Context(), "signing", time.Second); !errors.Is(err, coordination.ErrUnsafeStorage) {
		t.Fatal("recovered configuration silently resumed execution", err)
	}
}
