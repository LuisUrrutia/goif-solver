package coordination

import "errors"

// Control is the fleet's operational configuration. Global pause wins over a
// node override. Node concurrency can only reduce the process's startup bound.
type Control struct {
	Nodes   map[string]NodeControl `json:"nodes"`
	Version uint64                 `json:"version"`
	Paused  bool                   `json:"paused"`
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

func ValidateControl(expected uint64, c Control) error {
	if c.Version != expected+1 || c.Version > 9007199254740991 || len(c.Nodes) > 1000 {
		return errors.New("invalid control version or node count")
	}
	for node, override := range c.Nodes {
		if node == "" || len(node) > 128 || override.Workers < 0 || override.Workers > 32 {
			return errors.New("invalid node override")
		}
	}
	return nil
}
