package coordination

import (
	"errors"
	"testing"
)

func TestVersionedControlAndNodePrecedence(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	c := Control{Version: 1, Nodes: map[string]NodeControl{"slow": {Workers: 1}, "maintenance": {Paused: true}}}
	if err := s.SetControl(ctx, 0, c); err != nil {
		t.Fatal(err)
	}
	if err := s.SetControl(ctx, 0, c); !errors.Is(err, ErrConflict) {
		t.Fatal("stale config write accepted")
	}
	read, err := s.Control(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if read.Allows("slow", 1, 4) || !read.Allows("fast", 1, 4) || read.Allows("maintenance", 0, 4) {
		t.Fatal("node precedence failed")
	}
	read.Paused = true
	read.Version = 2
	if err = s.SetControl(ctx, 1, read); err != nil {
		t.Fatal(err)
	}
	if read.Allows("fast", 0, 4) {
		t.Fatal("global pause overridden")
	}
	if err = s.BindConfig(ctx, "policy-a"); err != nil {
		t.Fatal(err)
	}
	if err = s.BindConfig(ctx, "policy-b"); !errors.Is(err, ErrConflict) {
		t.Fatal("incompatible fleet policy accepted")
	}
}
