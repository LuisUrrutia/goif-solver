package app

import (
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/control"
	"github.com/LuisUrrutia/goif-solver/internal/oif"
	"github.com/LuisUrrutia/goif-solver/internal/solver"
)

const oifAPI config.Kind = "oif"

type oifSettings struct {
	TokenEnv          string `json:"token_env"`
	QuoteKeyEnv       string `json:"quote_key_env"`
	Provider          string `json:"provider"`
	RequestsPerSecond int    `json:"requests_per_second"`
}

func (r *Runtime) httpHandler(c config.Config, service *solver.Service, execute bool) (http.Handler, error) {
	mux := http.NewServeMux()
	mux.Handle("/", control.Handler(service, os.Getenv(c.ControlTokenEnv)))
	installed := map[config.Kind]bool{}
	for _, definition := range c.APIs {
		if installed[definition.Kind] {
			return nil, errors.New("duplicate public API")
		}
		installed[definition.Kind] = true
		switch definition.Kind {
		case oifAPI:
			settings, err := config.Decode[oifSettings](definition.Settings)
			if err != nil {
				return nil, fmt.Errorf("OIF API settings: %w", err)
			}
			if !config.ValidEnv(settings.TokenEnv) || !config.ValidEnv(settings.QuoteKeyEnv) || settings.TokenEnv == c.ControlTokenEnv || settings.Provider == "" || len(settings.Provider) > 128 || settings.RequestsPerSecond < 1 || settings.RequestsPerSecond > 1000 {
				return nil, errors.New("invalid OIF API settings")
			}
			token, err := config.Secret(settings.TokenEnv)
			if err != nil || len(token) < 32 {
				return nil, errors.New("OIF API token must contain at least 32 characters")
			}
			key, err := config.Secret(settings.QuoteKeyEnv)
			if err != nil || len(key) < 32 || key == token || key == os.Getenv(c.ControlTokenEnv) || token == os.Getenv(c.ControlTokenEnv) {
				return nil, errors.New("OIF quote key and API/control tokens must be distinct")
			}
			routes := make([]oif.Route, 0, len(definition.Routes))
			for _, binding := range definition.Routes {
				execution := r.Executions[binding.Protocol]
				if execution == nil || execution.OIF[binding.Name] == nil {
					return nil, errors.New("OIF route adapter unavailable")
				}
				route := execution.OIF[binding.Name]
				for _, network := range route.Assets() {
					for _, asset := range network.Assets {
						if asset.Symbol == "" || len(asset.Symbol) > 32 {
							return nil, errors.New("OIF assets require configured symbols")
						}
					}
				}
				routes = append(routes, route)
			}
			adapter := &oif.Handler{Store: service.Engine.Store, Prepare: service.Engine.Prepare, Accepted: func() { service.Discovered.Add(1) }, Routes: routes, Token: token, QuoteKey: []byte(key), Node: service.Node, Provider: settings.Provider, RequestsPerSecond: settings.RequestsPerSecond, Enabled: execute}
			mux.Handle("/v1/", adapter.HTTP())
		default:
			return nil, errors.New("public API adapter is not installed")
		}
	}
	return mux, nil
}
