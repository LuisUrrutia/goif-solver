package memorystore

import (
	"context"
	"errors"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
)

func (s *Store) availableAt(id string) time.Time {
	due := s.ready[id]
	if expiry := s.leases[coordination.IntentResource(id)].expires; expiry.After(due) {
		return expiry
	}
	return due
}

func (s *Store) ClaimNext(ctx context.Context, ttl time.Duration) (coordination.Claim, error) {
	if ttl < time.Millisecond {
		return coordination.Claim{}, errors.New("invalid intent claim TTL")
	}
	if err := s.lock(ctx); err != nil {
		return coordination.Claim{}, err
	}
	defer s.mu.Unlock()
	var selected string
	var first time.Time
	now := time.Now()
	for id := range s.ready {
		due := s.availableAt(id)
		if !due.After(now) && (selected == "" || due.Before(first) || due.Equal(first) && id < selected) {
			selected, first = id, due
		}
	}
	if selected == "" {
		return coordination.Claim{}, coordination.ErrNotReady
	}
	lease, err := s.acquireLocked(coordination.IntentResource(selected), ttl)
	return coordination.Claim{ID: selected, Lease: lease}, err
}

func (s *Store) notifyLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *Store) Wait(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("invalid queue wait interval")
	}
	if err := s.lock(ctx); err != nil {
		return err
	}
	changed := s.changed
	now := time.Now()
	for id := range s.ready {
		interval = min(interval, s.availableAt(id).Sub(now))
	}
	s.mu.Unlock()
	if interval <= 0 {
		return nil
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-changed:
		return nil
	case <-timer.C:
		return nil
	}
}
