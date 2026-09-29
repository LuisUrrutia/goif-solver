package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"go.uber.org/zap"
)

func TestOIFAPISelectsRoutesAndKeepsControlCredentialsSeparate(t *testing.T) {
	c, err := config.Load("../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	c.Development = true
	c.Storage = config.Storage{Kind: config.MemoryStorage}
	c.Sources = nil
	c.Publications = nil
	c.Providers = nil
	settings := oifSettings{TokenEnv: "TEST_OIF_API_TOKEN", QuoteKeyEnv: "TEST_OIF_QUOTE_KEY", Provider: "local", RequestsPerSecond: 1000}
	c.APIs = []config.API{{Definition: config.Definition{Kind: oifAPI, Settings: encodeSettings(t, settings)}, Routes: []config.Route{{Protocol: "evm-escrow", Name: "sepolia-base-usdc"}}}}
	token := strings.Repeat("a", 32)
	controlToken := strings.Repeat("c", 32)
	t.Setenv(settings.TokenEnv, token)
	t.Setenv(settings.QuoteKeyEnv, strings.Repeat("q", 32))
	t.Setenv(c.ControlTokenEnv, controlToken)
	service, err := New(t.Context(), c, "http-test", false, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	for _, test := range []struct {
		Path, Token string
		Status      int
	}{{"/v1/assets", token, http.StatusOK}, {"/control", token, http.StatusUnauthorized}, {"/v1/assets", controlToken, http.StatusUnauthorized}, {"/control", controlToken, http.StatusOK}} {
		req := httptest.NewRequest(http.MethodGet, test.Path, nil)
		req.Header.Set("Authorization", "Bearer "+test.Token)
		response := httptest.NewRecorder()
		service.Handler.ServeHTTP(response, req)
		if response.Code != test.Status {
			t.Fatal(test, response.Code, response.Body.String())
		}
	}
	c.APIs[0].Routes[0].Name = "unconfigured-route"
	if service, err := New(t.Context(), c, "bad-api", false, zap.NewNop()); err == nil {
		service.Close()
		t.Fatal("accepted unknown API route")
	}
}
