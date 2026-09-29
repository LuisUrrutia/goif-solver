package redisstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"

	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/redis/go-redis/v9"
)

// All keys share one Redis Cluster hash slot so transitions remain atomic.
// Use a separate namespace for each independent solver fleet.
type Store struct {
	client *redis.Client
	prefix string
}

func New(client *redis.Client, namespace string) (*Store, error) {
	if namespace == "" {
		return nil, errors.New("empty namespace")
	}
	for _, c := range namespace {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return nil, errors.New("namespace must be alphanumeric or hyphen")
		}
	}
	return &Store{client: client, prefix: "{" + namespace + "}:"}, nil
}

func (s *Store) key(kind, id string) string {
	h := sha256.Sum256([]byte(id))
	return s.prefix + kind + ":" + hex.EncodeToString(h[:])
}
func (s *Store) Ping(ctx context.Context) error { return s.client.Ping(ctx).Err() }

// Enqueue deduplicates discovery across sources and nodes. Payload must be a
// canonical immutable order; source timestamps do not belong in it.
func (s *Store) Enqueue(ctx context.Context, id, payload string) (bool, error) {
	if id == "" || payload == "" {
		return false, errors.New("empty order")
	}
	n, e := enqueue.Run(ctx, s.client, []string{s.key("order", id), s.prefix + "ready"}, id, payload, string(intent.Discovered)).Int()
	if e != nil {
		return false, e
	}
	if n < 0 {
		return false, coordination.ErrConflict
	}
	return n == 1, nil
}

func (s *Store) Record(ctx context.Context, id string) (coordination.Record, error) {
	m, e := s.client.HGetAll(ctx, s.key("order", id)).Result()
	if e != nil {
		return coordination.Record{}, e
	}
	if len(m) == 0 {
		return coordination.Record{}, coordination.ErrNotFound
	}
	return coordination.Record{ID: m["id"], Payload: m["payload"], Stage: intent.Stage(m["stage"]), Detail: m["detail"]}, nil
}

func (s *Store) Ready(ctx context.Context, limit int64) ([]string, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("ready limit must be 1..1000")
	}
	return ready.Run(ctx, s.client, []string{s.prefix + "ready"}, limit).StringSlice()
}

func (s *Store) Acquire(ctx context.Context, resource string, ttl time.Duration) (coordination.Lease, error) {
	if resource == "" || ttl < time.Millisecond {
		return coordination.Lease{}, errors.New("invalid lease")
	}
	n, e := acquire.Run(ctx, s.client, []string{s.key("lease", resource), s.key("fence", resource)}, ttl.Milliseconds()).Int64()
	if e != nil {
		return coordination.Lease{}, e
	}
	if n == 0 {
		return coordination.Lease{}, coordination.ErrBusy
	}
	return coordination.Lease{Resource: resource, Token: n}, nil
}

func (s *Store) Renew(ctx context.Context, l coordination.Lease, ttl time.Duration) error {
	if ttl < time.Millisecond {
		return errors.New("invalid lease TTL")
	}
	n, e := renew.Run(ctx, s.client, []string{s.key("lease", l.Resource)}, l.Token, ttl.Milliseconds()).Int()
	return fenced(n, e)
}

func (s *Store) Release(ctx context.Context, l coordination.Lease) error {
	n, e := release.Run(ctx, s.client, []string{s.key("lease", l.Resource)}, l.Token).Int()
	return fenced(n, e)
}

func fenced(n int, e error) error {
	if e != nil {
		return e
	}
	if n == 0 {
		return coordination.ErrLeaseLost
	}
	if n < 0 {
		return coordination.ErrConflict
	}
	return nil
}

// Advance performs a compare-and-swap under the current order lease. Terminal
// records remain durable for duplicate discovery suppression and reconciliation.
func (s *Store) Advance(ctx context.Context, l coordination.Lease, id string, from, to intent.Stage, detail string, terminal bool, delay time.Duration) error {
	if l.Resource != coordination.IntentResource(id) || from == "" || to == "" || delay < 0 {
		return errors.New("invalid transition")
	}
	done := 0
	if terminal {
		done = 1
	}
	n, e := advance.Run(ctx, s.client, []string{s.key("lease", l.Resource), s.key("order", id), s.prefix + "ready"}, l.Token, string(from), string(to), detail, done, id, delay.Milliseconds()).Int()
	return fenced(n, e)
}

// Prepare reserves the entire signer/chain until mined reconciliation. A lost
// lease alone never frees this reservation or permits another nonce decision.
func (s *Store) Prepare(ctx context.Context, order, signer coordination.Lease, tx coordination.Transaction) error {
	if tx.Operation == "" || tx.Raw == "" || tx.Hash == "" || !strings.HasPrefix(order.Resource, "order:") || !strings.HasPrefix(signer.Resource, "signer:") {
		return errors.New("invalid transaction reservation")
	}
	value := strconv.FormatUint(tx.Nonce, 10) + "|" + tx.Hash + "|" + tx.Raw
	n, e := prepare.Run(ctx, s.client, []string{s.key("lease", order.Resource), s.key("lease", signer.Resource), s.key("transactions", signer.Resource), s.key("pending", signer.Resource)}, order.Token, signer.Token, tx.Operation, value).Int()
	if n == -2 && e == nil {
		return coordination.ErrBusy
	}
	return fenced(n, e)
}

func (s *Store) Pending(ctx context.Context, signer string) (string, error) {
	v, e := s.client.Get(ctx, s.key("pending", signer)).Result()
	if errors.Is(e, redis.Nil) {
		return "", nil
	}
	return v, e
}

func (s *Store) Transaction(ctx context.Context, signer, operation string) (coordination.Transaction, error) {
	v, err := s.client.HGet(ctx, s.key("transactions", signer), operation).Result()
	if errors.Is(err, redis.Nil) {
		return coordination.Transaction{}, coordination.ErrNotFound
	}
	if err != nil {
		return coordination.Transaction{}, err
	}
	nonceText, rest, ok := strings.Cut(v, "|")
	if !ok {
		return coordination.Transaction{}, errors.New("corrupt transaction journal")
	}
	hash, raw, ok := strings.Cut(rest, "|")
	if !ok || hash == "" || raw == "" {
		return coordination.Transaction{}, errors.New("corrupt transaction journal")
	}
	nonce, err := strconv.ParseUint(nonceText, 10, 64)
	if err != nil {
		return coordination.Transaction{}, errors.New("corrupt transaction nonce")
	}
	return coordination.Transaction{Operation: operation, Nonce: nonce, Hash: hash, Raw: raw}, nil
}

// CompleteTransaction may only be called after a canonical receipt has reached
// configured finality. The journal is retained even when the transaction reverted.
func (s *Store) CompleteTransaction(ctx context.Context, signer coordination.Lease, operation string) error {
	n, e := complete.Run(ctx, s.client, []string{s.key("lease", signer.Resource), s.key("pending", signer.Resource)}, signer.Token, operation).Int()
	return fenced(n, e)
}
