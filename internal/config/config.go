// Package config validates public configuration and resolves secret references.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/LuisUrrutia/goif-solver/internal/evm"
	"github.com/ethereum/go-ethereum/common"
)

type Endpoint struct {
	URL string `json:"url,omitempty"`
	Env string `json:"env,omitempty"`
}
type Chain struct {
	ID             uint64     `json:"id"`
	RPCs           []Endpoint `json:"rpcs"`
	SigningEnabled bool       `json:"signing_enabled"`
	Confirmations  uint64     `json:"confirmations"`
	MaxGas         uint64     `json:"max_gas"`
	MaxFeeWei      string     `json:"max_fee_wei"`
}
type Signer struct {
	Name    string         `json:"name"`
	Address common.Address `json:"address"`
	KeyEnv  string         `json:"key_env"`
	Chains  []uint64       `json:"chains"`
}
type OrderSource struct {
	URL    string `json:"url"`
	KeyEnv string `json:"key_env,omitempty"`
}
type Config struct {
	OrderAllowlist    []common.Hash `json:"order_allowlist,omitempty"`
	OrderSources      []OrderSource `json:"order_sources,omitempty"`
	Version           uint64        `json:"version"`
	Namespace         string        `json:"namespace"`
	RedisEnv          string        `json:"redis_url_env"`
	Listen            string        `json:"listen"`
	ControlTokenEnv   string        `json:"control_token_env"`
	OrderAPI          string        `json:"order_api"`
	APIKeyEnv         string        `json:"api_key_env"`
	PolymerAPI        string        `json:"polymer_api"`
	PolymerKeyEnv     string        `json:"polymer_key_env"`
	PolymerRequest    string        `json:"polymer_request_method"`
	PolymerQuery      string        `json:"polymer_query_method"`
	RequestsPerSecond int           `json:"requests_per_second"`
	Workers           int           `json:"workers"`
	PollSeconds       int           `json:"poll_seconds"`
	Chains            []Chain       `json:"chains"`
	Signers           []Signer      `json:"signers"`
	Routes            []evm.Route   `json:"routes"`
}

var envName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

func Load(path string) (Config, error) {
	f, e := os.Open(path)
	if e != nil {
		return Config{}, errors.New("open configuration failed")
	}
	defer f.Close()
	var c Config
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if e = d.Decode(&c); e != nil {
		return c, errors.New("invalid configuration JSON")
	}
	var extra interface{}
	if d.Decode(&extra) != io.EOF {
		return c, errors.New("trailing configuration data")
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	if c.Version == 0 || !regexp.MustCompile(`^[A-Za-z0-9-]+$`).MatchString(c.Namespace) {
		return errors.New("version and namespace required")
	}
	if c.Workers < 1 || c.Workers > 32 || c.PollSeconds < 1 || c.PollSeconds > 300 || c.RequestsPerSecond < 1 || c.RequestsPerSecond > 100 {
		return errors.New("invalid worker, polling, or rate bounds")
	}
	for _, s := range []string{c.RedisEnv, c.ControlTokenEnv, c.APIKeyEnv, c.PolymerKeyEnv} {
		if !envName.MatchString(s) {
			return errors.New("invalid secret environment reference")
		}
	}
	if len(c.OrderAllowlist) > 1000 {
		return errors.New("order allowlist too large")
	}
	for _, id := range c.OrderAllowlist {
		if id == (common.Hash{}) {
			return errors.New("zero order ID in allowlist")
		}
	}
	if len(c.OrderSources) > 8 {
		return errors.New("at most eight order sources supported")
	}
	for _, source := range c.OrderSources {
		if source.URL == "" || source.KeyEnv != "" && !envName.MatchString(source.KeyEnv) {
			return errors.New("invalid order source")
		}
	}
	chains := map[uint64]bool{}
	for _, ch := range c.Chains {
		if chains[ch.ID] || ch.ID == 0 || ch.Confirmations < 1 || ch.Confirmations > 10000 || ch.MaxGas < 21000 || ch.MaxGas > 10000000 || len(ch.RPCs) == 0 || len(ch.RPCs) > 16 {
			return errors.New("invalid chain policy")
		}
		for _, endpoint := range ch.RPCs {
			if endpoint.URL == "" && endpoint.Env == "" || endpoint.Env != "" && !envName.MatchString(endpoint.Env) {
				return errors.New("invalid RPC endpoint reference")
			}
		}
		chains[ch.ID] = true
		cap, e := evm.Uint(ch.MaxFeeWei, 256)
		if e != nil || cap.Sign() == 0 {
			return errors.New("invalid gas fee cap")
		}
	}
	signers := map[string]Signer{}
	addresses := map[common.Address]bool{}
	for _, s := range c.Signers {
		if s.Name == "" || s.Address == (common.Address{}) || !envName.MatchString(s.KeyEnv) || addresses[s.Address] {
			return errors.New("invalid or duplicate signer")
		}
		if _, ok := signers[s.Name]; ok {
			return errors.New("duplicate signer name")
		}
		signers[s.Name] = s
		addresses[s.Address] = true
		if len(s.Chains) == 0 {
			return errors.New("signer has no chain policy")
		}
		for _, id := range s.Chains {
			if !chains[id] {
				return errors.New("unknown signer chain")
			}
		}
	}
	names := map[string]bool{}
	routes := map[string]bool{}
	for _, r := range c.Routes {
		if names[r.Name] || r.Name == "" || !chains[r.OriginChain] || !chains[r.DestinationChain] || r.OriginChain == r.DestinationChain || r.DeadlineBuffer < 30 {
			return errors.New("invalid route")
		}
		names[r.Name] = true
		pair := fmt.Sprintf("%d/%d/%s/%s", r.OriginChain, r.DestinationChain, r.InputToken.Hex(), r.OutputToken.Hex())
		if routes[pair] {
			return errors.New("duplicate route pair requires explicit selection strategy")
		}
		routes[pair] = true
		for _, a := range []common.Address{r.InputSettler, r.OutputSettler, r.InputOracle, r.OutputOracle, r.InputToken, r.OutputToken} {
			if a == (common.Address{}) {
				return errors.New("zero route contract")
			}
		}
		s, ok := signers[r.Signer]
		if !ok {
			return errors.New("unknown route signer")
		}
		for _, chain := range []uint64{r.OriginChain, r.DestinationChain} {
			found := false
			for _, allowed := range s.Chains {
				found = found || chain == allowed
			}
			if !found {
				return errors.New("route exceeds signer policy")
			}
		}
		for _, amount := range []string{r.MaxInput, r.MaxOutput, r.MinMargin} {
			if _, e := evm.Uint(amount, 256); e != nil {
				return fmt.Errorf("route %s has invalid amount", r.Name)
			}
		}
	}
	if len(c.Routes) == 0 {
		return errors.New("no routes configured")
	}
	return nil
}
func Secret(name string) (string, error) {
	if !envName.MatchString(name) {
		return "", errors.New("invalid environment reference")
	}
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("required environment variable %s is unset", name)
	}
	return v, nil
}
func (c Chain) URLs() ([]string, error) {
	endpoints := make([]string, 0, len(c.RPCs))
	for _, endpoint := range c.RPCs {
		value := os.Getenv(endpoint.Env)
		if value == "" {
			value = endpoint.URL
		}
		if value != "" {
			endpoints = append(endpoints, value)
		}
	}
	if len(endpoints) == 0 {
		return nil, errors.New("no RPC endpoint available")
	}
	return endpoints, nil
}

func (c Config) AllowsOrder(id common.Hash) bool {
	if len(c.OrderAllowlist) == 0 {
		return true
	}
	for _, allowed := range c.OrderAllowlist {
		if allowed == id {
			return true
		}
	}
	return false
}
