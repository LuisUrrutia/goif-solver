package coordination

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/redis/go-redis/v9"
)

// Control is the fleet's operational configuration. Global pause wins over a
// node override. Node concurrency can only reduce the process's startup bound.
type Control struct {
	Version uint64                 `json:"version"`
	Paused  bool                   `json:"paused"`
	Nodes   map[string]NodeControl `json:"nodes"`
}
type NodeControl struct {
	Paused  bool `json:"paused"`
	Workers int  `json:"workers"`
}

func (c Control) Allows(node string, worker, maximum int) bool {
	if c.Paused {
		return false
	}
	override, ok := c.Nodes[node]
	if !ok {
		return worker < maximum
	}
	if override.Paused {
		return false
	}
	if override.Workers > 0 && override.Workers < maximum {
		maximum = override.Workers
	}
	return worker < maximum
}
func (s *Store) Control(ctx context.Context) (Control, error) {
	b, err := s.client.Get(ctx, s.prefix+"control").Bytes()
	if errors.Is(err, redis.Nil) {
		return Control{Nodes: map[string]NodeControl{}}, nil
	}
	if err != nil {
		return Control{}, err
	}
	var c Control
	err = json.Unmarshal(b, &c)
	return c, err
}

var setControl = redis.NewScript(`
local current=redis.call('GET',KEYS[1]);local version=0
if current then version=cjson.decode(current).version end
if version~=tonumber(ARGV[1]) then return -1 end
redis.call('SET',KEYS[1],ARGV[2]);return 1`)

func (s *Store) SetControl(ctx context.Context, expected uint64, c Control) error {
	if c.Version != expected+1 || c.Version > 9007199254740991 || len(c.Nodes) > 1000 {
		return errors.New("invalid control version or node count")
	}
	for node, override := range c.Nodes {
		if node == "" || len(node) > 128 || override.Workers < 0 || override.Workers > 32 {
			return errors.New("invalid node override")
		}
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	n, err := setControl.Run(ctx, s.client, []string{s.prefix + "control"}, expected, string(b)).Int()
	return fenced(n, err)
}

var bindConfig = redis.NewScript(`
local old=redis.call('GET',KEYS[1]);if old and old~=ARGV[1] then return -1 end
redis.call('SET',KEYS[1],ARGV[1]);return 1`)

// BindConfig prevents nodes with different route or signer policies from joining
// the same namespace. Policy migrations use a drained namespace and new version.
func (s *Store) BindConfig(ctx context.Context, digest string) error {
	n, err := bindConfig.Run(ctx, s.client, []string{s.prefix + "config-digest"}, digest).Int()
	return fenced(n, err)
}
