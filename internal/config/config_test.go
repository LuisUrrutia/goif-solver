package config

import (
	"strings"
	"testing"

	"github.com/LuisUrrutia/goif-solver/internal/intent"
)

func TestPersistentStorageRequiresExternalPrimaryApproval(t *testing.T) {
	c, err := Load("../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	c.Storage.PrimaryRunIDEnv = ""
	if err := c.Validate(); err == nil {
		t.Fatal("Redis accepted without external primary identity")
	}
	c.Development = true
	if err := c.Validate(); err == nil {
		t.Fatal("development observer bypassed persistent storage approval")
	}
	c.Storage = Storage{Kind: MemoryStorage}
	if err := c.Validate(); err != nil {
		t.Fatal("memory development unexpectedly needs primary approval", err)
	}
}

func TestIntentAuthorizationIncludesProtocol(t *testing.T) {
	c, err := Load("../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	id := intent.Identity{Kind: "evm-escrow", NativeID: "native"}
	c.IntentAllowlist = []intent.Identity{id}
	if !c.AllowsIntent(id) || c.AllowsIntent(intent.Identity{Kind: "another-protocol", NativeID: id.NativeID}) || c.AllowsIntent(intent.Identity{Kind: id.Kind, NativeID: "another"}) {
		t.Fatal("intent scope not enforced")
	}
}

func TestProviderStreamIsSharedAcrossRoutes(t *testing.T) {
	c, err := Load("../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	duplicate := c.Sources[0]
	duplicate.Name = "another-network"
	c.Sources = append(c.Sources, duplicate)
	if err = c.Validate(); err == nil || !strings.Contains(err.Error(), "one shared stream") {
		t.Fatal("accepted duplicate provider subscription", err)
	}
}

func TestPublicationRequiresProviderRouteBinding(t *testing.T) {
	c, err := Load("../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	c.Publications[0].Route.Name = "unbound-route"
	if err = c.Validate(); err == nil {
		t.Fatal("publication ignored binding")
	}
}
