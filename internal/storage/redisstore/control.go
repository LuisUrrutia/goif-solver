package redisstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/LuisUrrutia/goif-solver/internal/coordination"

	"github.com/redis/go-redis/v9"
)

func (s *Store) Control(ctx context.Context) (coordination.Control, error) {
	b, err := s.client.Get(ctx, s.prefix+"control").Bytes()
	if errors.Is(err, redis.Nil) {
		return coordination.Control{Nodes: map[string]coordination.NodeControl{}}, nil
	}
	if err != nil {
		return coordination.Control{}, err
	}
	var c coordination.Control
	err = json.Unmarshal(b, &c)
	return c, err
}

func (s *Store) SetControl(ctx context.Context, expected uint64, c coordination.Control) error {
	if err := coordination.ValidateControl(expected, c); err != nil {
		return err
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	n, err := setControl.Run(ctx, s.client, []string{s.prefix + "control"}, expected, string(b)).Int()
	return fenced(n, err)
}

// BindConfig prevents nodes with different route or signer policies from joining
// the same namespace. Policy migrations use a drained namespace and new version.
func (s *Store) BindConfig(ctx context.Context, digest string) error {
	n, err := bindConfig.Run(ctx, s.client, []string{s.prefix + "config-digest"}, digest).Int()
	return fenced(n, err)
}
