package redisstore

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"strings"
	"sync/atomic"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/redis/go-redis/v9"
)

type primaryGuard struct {
	runID   string
	blocked atomic.Bool
}

type primaryConnection interface {
	Info(context.Context, ...string) *redis.StringCmd
	Process(context.Context, redis.Cmder) error
}

// Open pins every physical connection to an operator-approved Redis process.
// The expected identity must outlive pods and must never be learned on startup.
func Open(options *redis.Options, namespace, runID string) (*Store, func(), error) {
	identity, err := hex.DecodeString(runID)
	if err != nil || len(identity) != 20 {
		return nil, nil, errors.New("redis primary run ID must be 40 hexadecimal characters")
	}
	guard := &primaryGuard{runID: strings.ToLower(runID)}
	settings := *options
	settings.ContextTimeoutEnabled = true
	settings.OnConnect = func(ctx context.Context, conn *redis.Conn) error { return guard.check(ctx, conn) }
	client := redis.NewClient(&settings)
	client.AddHook(guard)
	closeStore := func() { _ = client.Close() }
	store, err := New(client, namespace)
	if err != nil {
		closeStore()
		return nil, nil, err
	}
	store.guard = guard
	return store, closeStore, nil
}

func (g *primaryGuard) reject() error {
	g.blocked.Store(true)
	return coordination.ErrUnsafeStorage
}

func InspectPrimary(ctx context.Context, options *redis.Options) (string, error) {
	client := redis.NewClient(options)
	defer func() { _ = client.Close() }()
	info, err := client.Info(ctx, "server").Result()
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(info, "\n") {
		if identity, found := strings.CutPrefix(strings.TrimSpace(line), "run_id:"); found {
			decoded, err := hex.DecodeString(identity)
			if err != nil || len(decoded) != 20 {
				return "", coordination.ErrUnsafeStorage
			}
			guard := &primaryGuard{runID: identity}
			return identity, guard.check(ctx, client)
		}
	}
	return "", coordination.ErrUnsafeStorage
}

func (g *primaryGuard) check(ctx context.Context, conn primaryConnection) error {
	if g.blocked.Load() {
		return coordination.ErrUnsafeStorage
	}
	info, err := conn.Info(ctx, "server", "replication", "persistence").Result()
	if err != nil {
		if redis.HasErrorPrefix(err, "NOPERM") {
			return g.reject()
		}
		return err
	}
	fields := make(map[string]string)
	for line := range strings.SplitSeq(info, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok {
			fields[key] = value
		}
	}
	if fields["run_id"] != g.runID || fields["redis_mode"] != "standalone" || fields["role"] != "master" || fields["aof_enabled"] != "1" || fields["aof_last_write_status"] != "ok" {
		return g.reject()
	}
	command := redis.NewMapStringStringCmd(ctx, "CONFIG", "GET", "appendonly", "appendfsync", "no-appendfsync-on-rewrite", "aof-load-truncated", "maxmemory-policy")
	if err := conn.Process(ctx, command); err != nil {
		if redis.HasErrorPrefix(err, "NOPERM") {
			return g.reject()
		}
		return err
	}
	settings, err := command.Result()
	if err != nil {
		return err
	}
	for key, want := range map[string]string{"appendonly": "yes", "appendfsync": "always", "no-appendfsync-on-rewrite": "no", "aof-load-truncated": "no", "maxmemory-policy": "noeviction"} {
		if settings[key] != want {
			return g.reject()
		}
	}
	return nil
}

func (g *primaryGuard) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if g.blocked.Load() {
			return nil, coordination.ErrUnsafeStorage
		}
		return next(ctx, network, addr)
	}
}

func (g *primaryGuard) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if g.blocked.Load() {
			return coordination.ErrUnsafeStorage
		}
		return next(ctx, cmd)
	}
}

func (g *primaryGuard) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, commands []redis.Cmder) error {
		if g.blocked.Load() {
			return coordination.ErrUnsafeStorage
		}
		return next(ctx, commands)
	}
}
