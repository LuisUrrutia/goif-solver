package coordination

import (
	"context"
	"errors"

	"github.com/redis/go-redis/v9"
)

func (s *Store) Checkpoint(ctx context.Context, source string) (string, error) {
	value, err := s.client.Get(ctx, s.key("checkpoint", source)).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	return value, err
}

// CommitCheckpoint cannot overwrite a concurrent scanner's newer checkpoint.
// Delivery is acknowledged before this CAS, so losing it only causes replay.
func (s *Store) CommitCheckpoint(ctx context.Context, source, before, after string) error {
	n, err := checkpoint.Run(ctx, s.client, []string{s.key("checkpoint", source)}, before, after).Int()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrConflict
	}
	return nil
}
