// Package coordination owns durable work and fenced leases in Redis.
package coordination

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	ErrNotFound  = errors.New("record not found")
	ErrLeaseLost = errors.New("lease lost")
	ErrConflict  = errors.New("immutable record conflict")
	ErrBusy      = errors.New("resource busy")
)

// All keys share one Redis Cluster hash slot so transitions remain atomic.
// Use a separate namespace for each independent solver fleet.
type Store struct {
	client *redis.Client
	prefix string
}
type Lease struct {
	Resource string
	Token    int64
}
type Record struct {
	ID      string `json:"id"`
	Payload string `json:"payload"`
	Stage   string `json:"stage"`
	Detail  string `json:"detail"`
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

var enqueue = redis.NewScript(`
local old = redis.call('HGET', KEYS[1], 'payload')
if old then
 if old ~= ARGV[2] then return -1 end
 return 0
end
redis.call('HSET', KEYS[1], 'id', ARGV[1], 'payload', ARGV[2], 'stage', 'discovered', 'detail', '')
redis.call('ZADD', KEYS[2], 0, ARGV[1])
return 1`)

// Enqueue deduplicates discovery across sources and nodes. Payload must be a
// canonical immutable order; source timestamps do not belong in it.
func (s *Store) Enqueue(ctx context.Context, id, payload string) (bool, error) {
	if id == "" || payload == "" {
		return false, errors.New("empty order")
	}
	n, e := enqueue.Run(ctx, s.client, []string{s.key("order", id), s.prefix + "ready"}, id, payload).Int()
	if e != nil {
		return false, e
	}
	if n < 0 {
		return false, ErrConflict
	}
	return n == 1, nil
}
func (s *Store) Record(ctx context.Context, id string) (Record, error) {
	m, e := s.client.HGetAll(ctx, s.key("order", id)).Result()
	if e != nil {
		return Record{}, e
	}
	if len(m) == 0 {
		return Record{}, ErrNotFound
	}
	return Record{ID: m["id"], Payload: m["payload"], Stage: m["stage"], Detail: m["detail"]}, nil
}
func (s *Store) Ready(ctx context.Context, limit int64) ([]string, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("ready limit must be 1..1000")
	}
	return ready.Run(ctx, s.client, []string{s.prefix + "ready"}, limit).StringSlice()
}

var ready = redis.NewScript(`
local t=redis.call('TIME');local now=t[1]*1000+math.floor(t[2]/1000)
return redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', now, 'LIMIT', 0, ARGV[1])`)
var acquire = redis.NewScript(`
if redis.call('EXISTS',KEYS[1])==1 then return 0 end
local token=redis.call('INCR',KEYS[2])
redis.call('SET',KEYS[1],token,'PX',ARGV[1])
return token`)

func (s *Store) Acquire(ctx context.Context, resource string, ttl time.Duration) (Lease, error) {
	if resource == "" || ttl < time.Millisecond {
		return Lease{}, errors.New("invalid lease")
	}
	n, e := acquire.Run(ctx, s.client, []string{s.key("lease", resource), s.key("fence", resource)}, ttl.Milliseconds()).Int64()
	if e != nil {
		return Lease{}, e
	}
	if n == 0 {
		return Lease{}, ErrBusy
	}
	return Lease{Resource: resource, Token: n}, nil
}

var renew = redis.NewScript(`
if redis.call('GET',KEYS[1])~=ARGV[1] then return 0 end
return redis.call('PEXPIRE',KEYS[1],ARGV[2])`)

func (s *Store) Renew(ctx context.Context, l Lease, ttl time.Duration) error {
	if ttl < time.Millisecond {
		return errors.New("invalid lease TTL")
	}
	n, e := renew.Run(ctx, s.client, []string{s.key("lease", l.Resource)}, l.Token, ttl.Milliseconds()).Int()
	return fenced(n, e)
}

var release = redis.NewScript(`
if redis.call('GET',KEYS[1])~=ARGV[1] then return 0 end
return redis.call('DEL',KEYS[1])`)

func (s *Store) Release(ctx context.Context, l Lease) error {
	n, e := release.Run(ctx, s.client, []string{s.key("lease", l.Resource)}, l.Token).Int()
	return fenced(n, e)
}
func fenced(n int, e error) error {
	if e != nil {
		return e
	}
	if n == 0 {
		return ErrLeaseLost
	}
	if n < 0 {
		return ErrConflict
	}
	return nil
}

var advance = redis.NewScript(`
if redis.call('GET',KEYS[1])~=ARGV[1] then return 0 end
if redis.call('HGET',KEYS[2],'stage')~=ARGV[2] then return -1 end
redis.call('HSET',KEYS[2],'stage',ARGV[3],'detail',ARGV[4])
if ARGV[5]=='1' then redis.call('ZREM',KEYS[3],ARGV[6])
else
 local t=redis.call('TIME');local now=t[1]*1000+math.floor(t[2]/1000)
 redis.call('ZADD',KEYS[3],now+tonumber(ARGV[7]),ARGV[6])
end
return 1`)

// Advance performs a compare-and-swap under the current order lease. Terminal
// records remain durable for duplicate discovery suppression and reconciliation.
func (s *Store) Advance(ctx context.Context, l Lease, id, from, to, detail string, terminal bool, delay time.Duration) error {
	if l.Resource != "order:"+id || from == "" || to == "" || delay < 0 {
		return errors.New("invalid transition")
	}
	done := 0
	if terminal {
		done = 1
	}
	n, e := advance.Run(ctx, s.client, []string{s.key("lease", l.Resource), s.key("order", id), s.prefix + "ready"}, l.Token, from, to, detail, done, id, delay.Milliseconds()).Int()
	return fenced(n, e)
}

// Transaction is immutable after preparation. The signed bytes are persisted
// before any network broadcast and reused after uncertain RPC outcomes.
type Transaction struct {
	Operation string
	Raw       string
	Hash      string
	Nonce     uint64
}

var prepare = redis.NewScript(`
if redis.call('GET',KEYS[1])~=ARGV[1] or redis.call('GET',KEYS[2])~=ARGV[2] then return 0 end
local existing=redis.call('HGET',KEYS[3],ARGV[3])
if existing then if existing==ARGV[4] then return 1 else return -1 end end
local pending=redis.call('GET',KEYS[4])
if pending and pending~=ARGV[3] then return -2 end
redis.call('HSET',KEYS[3],ARGV[3],ARGV[4])
redis.call('SET',KEYS[4],ARGV[3])
return 1`)

// Prepare reserves the entire signer/chain until mined reconciliation. A lost
// lease alone never frees this reservation or permits another nonce decision.
func (s *Store) Prepare(ctx context.Context, order, signer Lease, tx Transaction) error {
	if tx.Operation == "" || tx.Raw == "" || tx.Hash == "" || len(order.Resource) < 6 || order.Resource[:6] != "order:" || len(signer.Resource) < 7 || signer.Resource[:7] != "signer:" {
		return errors.New("invalid transaction reservation")
	}
	value := strconv.FormatUint(tx.Nonce, 10) + "|" + tx.Hash + "|" + tx.Raw
	n, e := prepare.Run(ctx, s.client, []string{s.key("lease", order.Resource), s.key("lease", signer.Resource), s.key("transactions", signer.Resource), s.key("pending", signer.Resource)}, order.Token, signer.Token, tx.Operation, value).Int()
	if n == -2 && e == nil {
		return ErrBusy
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
func (s *Store) Transaction(ctx context.Context, signer, operation string) (Transaction, error) {
	v, err := s.client.HGet(ctx, s.key("transactions", signer), operation).Result()
	if errors.Is(err, redis.Nil) {
		return Transaction{}, ErrNotFound
	}
	if err != nil {
		return Transaction{}, err
	}
	nonceText, rest, ok := strings.Cut(v, "|")
	if !ok {
		return Transaction{}, errors.New("corrupt transaction journal")
	}
	hash, raw, ok := strings.Cut(rest, "|")
	if !ok || hash == "" || raw == "" {
		return Transaction{}, errors.New("corrupt transaction journal")
	}
	nonce, err := strconv.ParseUint(nonceText, 10, 64)
	if err != nil {
		return Transaction{}, errors.New("corrupt transaction nonce")
	}
	return Transaction{Operation: operation, Nonce: nonce, Hash: hash, Raw: raw}, nil
}

var complete = redis.NewScript(`
if redis.call('GET',KEYS[1])~=ARGV[1] then return 0 end
local pending=redis.call('GET',KEYS[2])
if not pending then return 1 end
if pending~=ARGV[2] then return -1 end
redis.call('DEL',KEYS[2]);return 1`)

// CompleteTransaction may only be called after a canonical receipt has reached
// configured finality. The journal is retained even when the transaction reverted.
func (s *Store) CompleteTransaction(ctx context.Context, signer Lease, operation string) error {
	n, e := complete.Run(ctx, s.client, []string{s.key("lease", signer.Resource), s.key("pending", signer.Resource)}, signer.Token, operation).Int()
	return fenced(n, e)
}
