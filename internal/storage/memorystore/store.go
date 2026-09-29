// Package memorystore provides process-local coordination for development.
package memorystore

import (
	"context"
	"errors"
	"maps"
	"sort"
	"sync"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
)

type reservation struct {
	expires time.Time
	token   int64
}
type pendingTransaction struct {
	since     time.Time
	operation string
}

type (
	journalKey struct{ signer, operation string }
	Store      struct {
		records      map[string]coordination.Record
		ready        map[string]time.Time
		leases       map[string]reservation
		fences       map[string]int64
		transactions map[journalKey]coordination.Transaction
		outcomes     map[journalKey]coordination.Outcome
		pending      map[string]pendingTransaction
		checkpoints  map[string]string
		digest       string
		control      coordination.Control
		mu           sync.Mutex
		bound        bool
	}
)

var _ coordination.Backend = (*Store)(nil)

func New() *Store {
	return &Store{records: make(map[string]coordination.Record), ready: make(map[string]time.Time), leases: make(map[string]reservation), fences: make(map[string]int64), transactions: make(map[journalKey]coordination.Transaction), outcomes: make(map[journalKey]coordination.Outcome), pending: make(map[string]pendingTransaction), checkpoints: make(map[string]string), control: coordination.Control{Nodes: make(map[string]coordination.NodeControl)}}
}

func (s *Store) lock(ctx context.Context) error {
	s.mu.Lock()
	if err := ctx.Err(); err != nil {
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *Store) valid(l coordination.Lease) bool {
	r, ok := s.leases[l.Resource]
	return ok && r.token == l.Token && time.Now().Before(r.expires)
}
func (s *Store) Ping(ctx context.Context) error { return ctx.Err() }
func (s *Store) Enqueue(ctx context.Context, id, payload string) (bool, error) {
	if id == "" || payload == "" {
		return false, errors.New("empty intent")
	}
	if err := s.lock(ctx); err != nil {
		return false, err
	}
	defer s.mu.Unlock()
	if r, ok := s.records[id]; ok {
		if r.Payload != payload {
			return false, coordination.ErrConflict
		}
		return false, nil
	}
	now := time.Now().UnixMilli()
	s.records[id] = coordination.Record{ID: id, Payload: payload, Stage: intent.Discovered, CreatedAt: now, UpdatedAt: now}
	s.ready[id] = time.Now()
	return true, nil
}

func (s *Store) Record(ctx context.Context, id string) (coordination.Record, error) {
	if err := s.lock(ctx); err != nil {
		return coordination.Record{}, err
	}
	defer s.mu.Unlock()
	r, ok := s.records[id]
	if !ok {
		return r, coordination.ErrNotFound
	}
	return r, nil
}

func (s *Store) Ready(ctx context.Context, limit, offset int64) ([]string, error) {
	if limit < 1 || limit > 1000 || offset < 0 {
		return nil, errors.New("ready limit must be 1..1000 and offset nonnegative")
	}
	if err := s.lock(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	now := time.Now()
	ids := make([]string, 0)
	for id, at := range s.ready {
		if !at.After(now) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := s.ready[ids[i]], s.ready[ids[j]]
		if a.Equal(b) {
			return ids[i] < ids[j]
		}
		return a.Before(b)
	})
	start := min(offset, int64(len(ids)))
	ids = ids[start:min(start+limit, int64(len(ids)))]
	return ids, nil
}

func (s *Store) Acquire(ctx context.Context, resource string, ttl time.Duration) (coordination.Lease, error) {
	if resource == "" || ttl < time.Millisecond {
		return coordination.Lease{}, errors.New("invalid lease")
	}
	if err := s.lock(ctx); err != nil {
		return coordination.Lease{}, err
	}
	defer s.mu.Unlock()
	if r, ok := s.leases[resource]; ok && time.Now().Before(r.expires) {
		return coordination.Lease{}, coordination.ErrBusy
	}
	s.fences[resource]++
	token := s.fences[resource]
	s.leases[resource] = reservation{expires: time.Now().Add(ttl), token: token}
	return coordination.Lease{Resource: resource, Token: token}, nil
}

func (s *Store) Renew(ctx context.Context, l coordination.Lease, ttl time.Duration) error {
	if ttl < time.Millisecond {
		return errors.New("invalid lease TTL")
	}
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if !s.valid(l) {
		return coordination.ErrLeaseLost
	}
	s.leases[l.Resource] = reservation{expires: time.Now().Add(ttl), token: l.Token}
	return nil
}

func (s *Store) Release(ctx context.Context, l coordination.Lease) error {
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if !s.valid(l) {
		return coordination.ErrLeaseLost
	}
	delete(s.leases, l.Resource)
	return nil
}

func (s *Store) Advance(ctx context.Context, l coordination.Lease, id string, from, to intent.Stage, detail string, terminal bool, delay time.Duration) error {
	if l.Resource != coordination.IntentResource(id) || from == "" || to == "" || delay < 0 {
		return errors.New("invalid transition")
	}
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if !s.valid(l) {
		return coordination.ErrLeaseLost
	}
	r, ok := s.records[id]
	if !ok || r.Stage != from {
		return coordination.ErrConflict
	}
	r.UpdatedAt = max(r.UpdatedAt, time.Now().UnixMilli())
	r.Stage = to
	r.Detail = detail
	s.records[id] = r
	if terminal {
		delete(s.ready, id)
	} else {
		s.ready[id] = time.Now().Add(delay)
	}
	return nil
}

func (s *Store) Prepare(ctx context.Context, work, signer coordination.Lease, tx coordination.Transaction) error {
	if tx.Validate() != nil || !coordination.IsIntentResource(work.Resource) || !coordination.IsSignerResource(signer.Resource) {
		return errors.New("invalid transaction reservation")
	}
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if !s.valid(work) || !s.valid(signer) {
		return coordination.ErrLeaseLost
	}
	key := journalKey{signer: signer.Resource, operation: tx.Operation}
	if existing, ok := s.transactions[key]; ok {
		if existing != tx {
			return coordination.ErrConflict
		}
		return nil
	}
	if pending := s.pending[signer.Resource].operation; pending != "" && pending != tx.Operation {
		return coordination.ErrBusy
	}
	s.transactions[key] = tx
	s.pending[signer.Resource] = pendingTransaction{since: time.Now(), operation: tx.Operation}
	return nil
}

func (s *Store) Pending(ctx context.Context, signer string) (string, error) {
	if err := s.lock(ctx); err != nil {
		return "", err
	}
	defer s.mu.Unlock()
	return s.pending[signer].operation, nil
}

func (s *Store) Transaction(ctx context.Context, signer, operation string) (coordination.Transaction, error) {
	if err := s.lock(ctx); err != nil {
		return coordination.Transaction{}, err
	}
	defer s.mu.Unlock()
	tx, ok := s.transactions[journalKey{signer: signer, operation: operation}]
	if !ok {
		return tx, coordination.ErrNotFound
	}
	return tx, nil
}

func (s *Store) CompleteTransaction(ctx context.Context, signer coordination.Lease, operation string, outcome coordination.Outcome) error {
	if err := outcome.Validate(); err != nil {
		return err
	}
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if !s.valid(signer) {
		return coordination.ErrLeaseLost
	}
	key := journalKey{signer: signer.Resource, operation: operation}
	if old, ok := s.outcomes[key]; ok {
		if old != outcome {
			return coordination.ErrConflict
		}
		return nil
	}
	if _, ok := s.transactions[key]; !ok || s.pending[signer.Resource].operation != operation {
		return coordination.ErrConflict
	}
	s.outcomes[key] = outcome
	delete(s.pending, signer.Resource)
	return nil
}

func (s *Store) TransactionOutcome(ctx context.Context, signer, operation string) (coordination.Outcome, error) {
	if err := s.lock(ctx); err != nil {
		return coordination.Outcome{}, err
	}
	defer s.mu.Unlock()
	outcome, ok := s.outcomes[journalKey{signer: signer, operation: operation}]
	if !ok {
		return coordination.Outcome{}, coordination.ErrNotFound
	}
	return outcome, nil
}

func (s *Store) Control(ctx context.Context) (coordination.Control, error) {
	if err := s.lock(ctx); err != nil {
		return coordination.Control{}, err
	}
	defer s.mu.Unlock()
	c := s.control
	c.Nodes = maps.Clone(c.Nodes)
	return c, nil
}

func (s *Store) SetControl(ctx context.Context, expected uint64, c coordination.Control) error {
	if err := coordination.ValidateControl(expected, c); err != nil {
		return err
	}
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if s.control.Version != expected {
		return coordination.ErrConflict
	}
	c.Nodes = maps.Clone(c.Nodes)
	s.control = c
	return nil
}

func (s *Store) BindConfig(ctx context.Context, digest string) error {
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if s.bound && s.digest != digest {
		return coordination.ErrConflict
	}
	s.digest = digest
	s.bound = true
	return nil
}

func (s *Store) Checkpoint(ctx context.Context, source string) (string, error) {
	if err := s.lock(ctx); err != nil {
		return "", err
	}
	defer s.mu.Unlock()
	return s.checkpoints[source], nil
}

func (s *Store) CommitCheckpoint(ctx context.Context, source, before, after string) error {
	if err := s.lock(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if s.checkpoints[source] != before {
		return coordination.ErrConflict
	}
	s.checkpoints[source] = after
	return nil
}

func (s *Store) Stats(ctx context.Context) (coordination.QueueStats, error) {
	if err := s.lock(ctx); err != nil {
		return coordination.QueueStats{}, err
	}
	defer s.mu.Unlock()
	result := coordination.QueueStats{Outstanding: int64(len(s.ready)), PendingSigners: int64(len(s.pending))}
	now := time.Now()
	for _, due := range s.ready {
		if !due.After(now) {
			result.Due++
			result.OldestDueMillis = max(result.OldestDueMillis, now.Sub(due).Milliseconds())
		}
	}
	for _, pending := range s.pending {
		result.OldestPendingMillis = max(result.OldestPendingMillis, now.Sub(pending.since).Milliseconds())
	}
	return result, nil
}
