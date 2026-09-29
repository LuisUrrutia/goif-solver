package app

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/escrow"
	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/storage/memorystore"
	"github.com/ethereum/go-ethereum/crypto"
	"go.uber.org/zap"
)

type alternativeExecution struct{}

func (alternativeExecution) Prepare(c intent.Candidate) (intent.Candidate, error) { return c, nil }
func (alternativeExecution) Step(context.Context, coordination.Lease, coordination.Record) error {
	return nil
}
func (alternativeExecution) Recover(context.Context) error { return nil }

func TestRuntimeSelectsNonEVMExecutionWithoutBuiltinInitialization(t *testing.T) {
	c, err := config.Load("../../config/development.json")
	if err != nil {
		t.Fatal(err)
	}
	const kind intent.Kind = "alternative-vm"
	c.Executions = map[intent.Kind]json.RawMessage{kind: json.RawMessage(`{"account":"base58-identity"}`)}
	closed := false
	service, err := NewWithFactories(t.Context(), c, "alternative", false, zap.NewNop(), map[intent.Kind]Factory{
		kind: func(_ context.Context, _ config.Config, raw json.RawMessage, _ coordination.Backend, execute bool, _ *zap.Logger) (*Execution, error) {
			if execute || string(raw) != string(c.Executions[kind]) {
				t.Fatal("settings changed")
			}
			return &Execution{Executor: alternativeExecution{}, Policy: raw, Close: func() { closed = true }}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := service.Engine.Prepare(intent.Candidate{Kind: kind, ID: "base58-native-id", Payload: json.RawMessage(`{}`)})
	if err != nil || prepared.Kind != kind || len(service.Engine.Executors) != 1 {
		t.Fatal(prepared, err)
	}
	service.Close()
	if !closed {
		t.Fatal("adapter leaked")
	}
}

func TestFleetPolicyIgnoresTransportAndLocalTuningButBindsExecution(t *testing.T) {
	load := func() config.Config {
		c, err := config.Load("../../config/testnet.json")
		if err != nil {
			t.Fatal(err)
		}
		c.Sources = c.Sources[1:]
		c.Publications = nil
		c.Providers = nil
		return c
	}
	bind := func(c config.Config, store coordination.Backend) error {
		runtime, err := Open(t.Context(), c, store, false, zap.NewNop(), builtins())
		if err != nil {
			t.Fatal(err)
		}
		defer runtime.Close()
		return runtime.Bind(t.Context(), store, c)
	}
	store := memorystore.New()
	c := load()
	if err := bind(c, store); err != nil {
		t.Fatal(err)
	}
	d := deployment(t, c)
	d.Chains[0].RPCs = []evm.Endpoint{{URL: "https://another.invalid", RequestsPerSecond: 25}}
	d.Chains[0].RequestsPerSecond = 12
	d.Routes[0].InputSymbol = "DISPLAY-ONLY"
	d.Signers[0].Custody.Settings = encodeSettings(t, map[string]string{"key_env": "OTHER_SIGNING_KEY"})
	d.Chains[0], d.Chains[1] = d.Chains[1], d.Chains[0]
	setDeployment(t, &c, d)
	c.Workers = 9
	c.WorkIntervalSeconds = 300
	c.RequestsPerSecond = 7
	c.Listen = "127.0.0.1:1"
	for id, backend := range c.Settlements {
		settings, err := config.Decode[polymerSettings](backend.Settings)
		if err != nil {
			t.Fatal(err)
		}
		settings.API = "https://another-proof.invalid"
		settings.KeyEnv = "OTHER_PROOF_KEY"
		settings.RequestsPerSecond = 7
		backend.Settings = encodeSettings(t, settings)
		c.Settlements[id] = backend
	}
	if err := bind(c, store); err != nil {
		t.Fatal("operational changes changed policy", err)
	}
	for _, mutate := range []func(*config.Config){
		func(c *config.Config) {
			d := deployment(t, *c)
			d.Routes[0].Pricing.MinMargin = "20000"
			setDeployment(t, c, d)
		},
		func(c *config.Config) { d := deployment(t, *c); d.Chains[0].Confirmations++; setDeployment(t, c, d) },
		func(c *config.Config) {
			c.IntentAllowlist = []intent.Identity{{Kind: "evm-escrow", NativeID: "0x1234"}}
		},
		func(c *config.Config) {
			for id, b := range c.Settlements {
				s, err := config.Decode[polymerSettings](b.Settings)
				if err != nil {
					t.Fatal(err)
				}
				s.QueryMethod = "anotherMethod"
				b.Settings = encodeSettings(t, s)
				c.Settlements[id] = b
			}
		},
	} {
		changed := load()
		mutate(&changed)
		if err := bind(changed, store); err == nil {
			t.Fatal("execution policy change was accepted")
		}
	}
}

func TestLIFIBindingsExcludeOtherRoutesAndCustody(t *testing.T) {
	c, err := config.Load("../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	d := deployment(t, c)
	other := d.Routes[0]
	other.Name = "private-route"
	other.Signer = "private-custody"
	d.Routes = append(d.Routes, other)
	signer := d.Signers[0]
	signer.Name = other.Signer
	signer.Custody.Kind = "uninstalled"
	d.Signers = append(d.Signers, signer)
	setDeployment(t, &c, d)
	selected, err := boundLIFIDeployment(c, c.Providers["lifi"].Routes)
	if err != nil || len(selected.Routes) != 1 || len(selected.Signers) != 1 || selected.Signers[0].Name == other.Signer {
		t.Fatal(selected, err)
	}
	if _, err = lifiProvider(c, c.Providers["lifi"], zap.NewNop()); err != nil {
		t.Fatal("unbound route affected provider", err)
	}
}

func TestFleetPolicyCanonicalizesObjectFieldsWithoutLosingIntegers(t *testing.T) {
	store := memorystore.New()
	c := config.Config{Version: config.SchemaVersion}
	runtime := Runtime{Executions: map[intent.Kind]*Execution{"sample": {Policy: json.RawMessage(`{"a":9007199254740993,"b":{"x":1,"y":2}}`)}}}
	if err := runtime.Bind(t.Context(), store, c); err != nil {
		t.Fatal(err)
	}
	runtime.Executions["sample"].Policy = json.RawMessage(`{"b":{"y":2,"x":1},"a":9007199254740993}`)
	if err := runtime.Bind(t.Context(), store, c); err != nil {
		t.Fatal("field order changed digest", err)
	}
	runtime.Executions["sample"].Policy = json.RawMessage(`{"a":9007199254740992,"b":{"x":1,"y":2}}`)
	if err := runtime.Bind(t.Context(), store, c); err == nil {
		t.Fatal("integer precision lost in policy digest")
	}
}

func TestExecutionCreatesSendersOnlyForRouteNetworks(t *testing.T) {
	c, err := config.Load("../../config/testnet.json")
	if err != nil {
		t.Fatal(err)
	}
	c.Sources = c.Sources[1:]
	c.Publications = nil
	c.Providers = nil
	d := deployment(t, c)
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	d.Signers[0].Address = crypto.PubkeyToAddress(key.PublicKey)
	d.Signers[0].Chains = append(d.Signers[0].Chains, 1337)
	d.Chains = append(d.Chains, evm.Chain{ID: 1337, RPCs: []evm.Endpoint{{Env: "UNSET_UNUSED_RPC"}}, Confirmations: 1, MaxGas: 100000, MaxFeeWei: "100"})
	t.Setenv("UNSET_UNUSED_RPC", "")
	t.Setenv("SOLVER_PRIVATE_KEY", hex.EncodeToString(crypto.FromECDSA(key)))
	t.Setenv("POLYMER_API_KEY", "synthetic-proof-key")
	setDeployment(t, &c, d)
	runtime, err := Open(t.Context(), c, memorystore.New(), true, zap.NewNop(), builtins())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	engine := runtime.Executions["evm-escrow"].Executor.(*escrow.Engine)
	senders := engine.Senders[d.Signers[0].Name]
	if len(senders) != 2 || senders[1337] != nil {
		t.Fatal("created an unused network sender")
	}
	for _, sender := range senders {
		if sender.Client == nil {
			t.Fatal("sender has no configured client")
		}
	}
}
