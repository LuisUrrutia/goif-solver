package app

import (
	"encoding/json"
	"testing"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	escrowprotocol "github.com/LuisUrrutia/goif-solver/internal/protocol/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"go.uber.org/zap"
)

func deployment(t *testing.T, c config.Config) escrowprotocol.Deployment {
	t.Helper()
	d, err := config.Decode[escrowprotocol.Deployment](c.Executions[escrowprotocol.IntentKind])
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func encodeSettings(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func setDeployment(t *testing.T, c *config.Config, d escrowprotocol.Deployment) {
	t.Helper()
	c.Executions[escrowprotocol.IntentKind] = encodeSettings(t, d)
}

func setLIFI(t *testing.T, c *config.Config, settings lifiSettings) {
	t.Helper()
	p := c.Providers["lifi"]
	p.Settings = encodeSettings(t, settings)
	c.Providers["lifi"] = p
}

func TestSelectedRoutesRequireConfiguredSettlement(t *testing.T) {
	for _, id := range []settlement.ID{"", "unknown"} {
		c, err := config.Load("../../config/testnet.json")
		if err != nil {
			t.Fatal(err)
		}
		d := deployment(t, c)
		d.Routes[0].Settlement = id
		setDeployment(t, &c, d)
		if runtime, err := Open(t.Context(), c, nil, false, zap.NewNop(), builtins()); err == nil {
			runtime.Close()
			t.Fatal("accepted missing settlement", id)
		}
	}
}

func TestSelectedSettlementRequiresTypedSettings(t *testing.T) {
	c, err := config.Load("../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	c.Settlements[deployment(t, c).Routes[0].Settlement] = config.Definition{Kind: polymerKind, Settings: json.RawMessage(`{}`)}
	if runtime, err := Open(t.Context(), c, nil, false, zap.NewNop(), builtins()); err == nil {
		runtime.Close()
		t.Fatal("accepted absent settlement settings")
	}
}

func TestSelectedSettlementRejectsInvalidProofRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		requests int
		seconds  int32
	}{
		{"negative requests", -1, 30},
		{"excessive requests", 101, 30},
		{"negative delay", 3, -1},
		{"excessive delay", 3, 301},
		{"large delay", 3, 2147483647},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := config.Load("../../config/testnet.json")
			if err != nil {
				t.Fatal(err)
			}
			c.Providers, c.Publications = nil, nil
			c.Sources = c.Sources[1:]
			id := deployment(t, c).Routes[0].Settlement
			definition := c.Settlements[id]
			settings, err := config.Decode[polymerSettings](definition.Settings)
			if err != nil {
				t.Fatal(err)
			}
			settings.MaxProofJobs, settings.ProofRetrySeconds = tc.requests, tc.seconds
			definition.Settings = encodeSettings(t, settings)
			c.Settlements[id] = definition

			runtime, err := Open(t.Context(), c, nil, false, zap.NewNop(), builtins())

			if err == nil {
				runtime.Close()
				t.Fatal("accepted invalid proof retry policy")
			}
		})
	}
}
