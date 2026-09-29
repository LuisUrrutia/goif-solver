package escrow

import (
	"github.com/LuisUrrutia/goif-solver/internal/config"
	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"
)

func loadTestPolicy(path string) (Policy, error) {
	c, err := config.Load(path)
	if err != nil {
		return Policy{}, err
	}
	d, err := config.Decode[escrowprotocol.Deployment](c.Executions[escrowprotocol.IntentKind])
	return Policy{Deployment: d, Version: c.Version, IntentAllowlist: c.IntentAllowlist}, err
}
