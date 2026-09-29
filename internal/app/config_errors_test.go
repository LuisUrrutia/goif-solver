package app

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/solver"
	"go.uber.org/zap"
)

func TestAdapterSettingsPreserveDecodeErrors(t *testing.T) {
	for _, adapter := range []string{"LI.FI", "LI.FI stream", "Polymer", "OIF"} {
		t.Run(adapter, func(t *testing.T) {
			c, err := config.Load("../../config/testnet.json")
			if err != nil {
				t.Fatal(err)
			}
			invalid := json.RawMessage(`{"unknown": !}`)

			switch adapter {
			case "LI.FI":
				definition := c.Providers["lifi"]
				definition.Settings = invalid
				_, err = lifiProvider(c, definition, zap.NewNop())
			case "LI.FI stream":
				provider, openErr := lifiProvider(c, c.Providers["lifi"], zap.NewNop())
				if openErr != nil {
					t.Fatal(openErr)
				}
				_, err = provider.stream(config.Source{Settings: invalid})
			case "Polymer":
				d := deployment(t, c)
				c.Settlements[d.Routes[0].Settlement] = config.Definition{Kind: polymerKind, Settings: invalid}
				_, err = configureSettlements(c, d, nil, nil, false)
			case "OIF":
				c.APIs = []config.API{{Definition: config.Definition{Kind: oifAPI, Settings: invalid}}}
				_, err = (&Runtime{}).httpHandler(c, &solver.Service{}, false)
			}

			var syntax *json.SyntaxError
			if !errors.As(err, &syntax) || !strings.Contains(err.Error(), adapter) {
				t.Fatalf("adapter or decode cause lost: %v", err)
			}
		})
	}
}
