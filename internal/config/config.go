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

type Chain struct {
	ID            uint64 `json:"id"`
	RPCEnv        string `json:"rpc_env"`
	PublicRPC     string `json:"public_rpc"`
	Confirmations uint64 `json:"confirmations"`
	MaxGas        uint64 `json:"max_gas"`
	MaxFeeWei     string `json:"max_fee_wei"`
}
type Signer struct {
	Name    string         `json:"name"`
	Address common.Address `json:"address"`
	KeyEnv  string         `json:"key_env"`
	Chains  []uint64       `json:"chains"`
}
type Config struct {
	Version           uint64      `json:"version"`
	Namespace         string      `json:"namespace"`
	RedisEnv          string      `json:"redis_url_env"`
	Listen            string      `json:"listen"`
	ControlTokenEnv   string      `json:"control_token_env"`
	OrderAPI          string      `json:"order_api"`
	APIKeyEnv         string      `json:"api_key_env"`
	PolymerAPI        string      `json:"polymer_api"`
	PolymerKeyEnv     string      `json:"polymer_key_env"`
	PolymerRequest    string      `json:"polymer_request_method"`
	PolymerQuery      string      `json:"polymer_query_method"`
	RequestsPerSecond int         `json:"requests_per_second"`
	Workers           int         `json:"workers"`
	PollSeconds       int         `json:"poll_seconds"`
	Chains            []Chain     `json:"chains"`
	Signers           []Signer    `json:"signers"`
	Routes            []evm.Route `json:"routes"`
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
	chains := map[uint64]bool{}
	for _, ch := range c.Chains {
		if chains[ch.ID] || ch.ID == 0 || ch.Confirmations < 1 || ch.Confirmations > 10000 || ch.MaxGas < 21000 || ch.MaxGas > 10000000 || !envName.MatchString(ch.RPCEnv) {
			return errors.New("invalid chain policy")
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
	for _, r := range c.Routes {
		if names[r.Name] || r.Name == "" || !chains[r.OriginChain] || !chains[r.DestinationChain] || r.OriginChain == r.DestinationChain || r.DeadlineBuffer < 30 {
			return errors.New("invalid route")
		}
		names[r.Name] = true
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
func (c Chain) URL() (string, error) {
	if v := os.Getenv(c.RPCEnv); v != "" {
		return v, nil
	}
	if c.PublicRPC == "" {
		return "", errors.New("RPC environment variable is unset")
	}
	return c.PublicRPC, nil
}
