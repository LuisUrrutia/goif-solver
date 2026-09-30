package redisstore

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"
)

// Candidates and their hashed keys are passed explicitly so scripts never
// derive key names from untrusted intent IDs. The lease decision is atomic.
func (s *Store) ClaimNext(ctx context.Context, ttl time.Duration) (coordination.Claim, error) {
	if ttl < time.Millisecond {
		return coordination.Claim{}, errors.New("invalid intent claim TTL")
	}
	const batchSize = 64
	for ctx.Err() == nil {
		ids, err := s.Ready(ctx, batchSize, 0)
		if err != nil {
			return coordination.Claim{}, err
		}
		if len(ids) == 0 {
			return coordination.Claim{}, coordination.ErrNotReady
		}
		keys := make([]string, 2, 2+2*len(ids))
		keys[0], keys[1] = s.prefix+"ready", s.prefix+"schedule"
		args := make([]interface{}, 1, 1+len(ids))
		args[0] = ttl.Milliseconds()
		for _, id := range ids {
			resource := coordination.IntentResource(id)
			keys = append(keys, s.key("lease", resource), s.key("fence", resource))
			args = append(args, id)
		}
		result, err := claimNext.Run(ctx, s.client, keys, args...).StringSlice()
		if err != nil {
			return coordination.Claim{}, err
		}
		if len(result) == 0 {
			continue
		}
		if len(result) != 2 {
			return coordination.Claim{}, errors.New("invalid intent claim result")
		}
		token, err := strconv.ParseInt(result[1], 10, 64)
		if err != nil || token < 1 {
			return coordination.Claim{}, errors.New("invalid intent fence")
		}
		return coordination.Claim{ID: result[0], Lease: coordination.Lease{Resource: coordination.IntentResource(result[0]), Token: token}}, nil
	}
	return coordination.Claim{}, ctx.Err()
}

func (s *Store) Wait(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("invalid queue wait interval")
	}
	subscription := s.client.Subscribe(ctx, s.prefix+"wake")
	defer func() { _ = subscription.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = subscription.Close() })
	defer stop()
	if _, err := subscription.Receive(ctx); err != nil {
		return err
	}
	// Subscribe before reading the deadline: enqueue can race either operation.
	delay, err := waitQueue.Run(ctx, s.client, []string{s.prefix + "ready"}, interval.Milliseconds()).Int64()
	if err != nil || delay <= 0 {
		return err
	}
	timer := time.NewTimer(time.Duration(delay) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-subscription.Channel():
		return nil
	case <-timer.C:
		return nil
	}
}

func intentID(resource string) string {
	if !coordination.IsIntentResource(resource) {
		return ""
	}
	return strings.TrimPrefix(resource, coordination.IntentResource(""))
}
