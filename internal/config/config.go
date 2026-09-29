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
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
	"github.com/ethereum/go-ethereum/common"
)

type Endpoint struct {
	URL string `json:"url,omitempty"`
	Env string `json:"env,omitempty"`
}
type Chain struct {
	MaxFeeWei      string     `json:"max_fee_wei"`
	RPCs           []Endpoint `json:"rpcs"`
	ID             uint64     `json:"id"`
	Confirmations  uint64     `json:"confirmations"`
	MaxGas         uint64     `json:"max_gas"`
	SigningEnabled bool       `json:"signing_enabled"`
}
type Signer struct {
	Name    string         `json:"name"`
	KeyEnv  string         `json:"key_env"`
	Chains  []uint64       `json:"chains"`
	Address common.Address `json:"address"`
}
type SourceKind string

const (
	LIFIWebSocket SourceKind = "lifi-websocket"
	EVMLogs       SourceKind = "evm-logs"
)

type IntentSource struct {
	Name            string         `json:"name"`
	Kind            SourceKind     `json:"kind"`
	URL             string         `json:"url,omitempty"`
	KeyEnv          string         `json:"key_env,omitempty"`
	ChainID         uint64         `json:"chain_id,omitempty"`
	Settler         common.Address `json:"settler,omitempty"`
	StartBlock      uint64         `json:"start_block,omitempty"`
	Lookback        uint64         `json:"lookback,omitempty"`
	IntervalSeconds int            `json:"interval_seconds,omitempty"`
}
type StorageKind string

const (
	RedisStorage  StorageKind = "redis"
	MemoryStorage StorageKind = "memory"
)

type Storage struct {
	Kind   StorageKind `json:"kind"`
	URLEnv string      `json:"url_env,omitempty"`
}
type LIFI struct {
	API    string `json:"api"`
	KeyEnv string `json:"key_env"`
}
type Providers struct {
	LIFI *LIFI `json:"lifi,omitempty"`
}
type PublisherKind string

const LIFIPublisher PublisherKind = "lifi"

type SettlementKind string

const PolymerSettlement SettlementKind = "polymer"

type PolymerSettings struct {
	API           string `json:"api"`
	KeyEnv        string `json:"key_env"`
	RequestMethod string `json:"request_method"`
	QueryMethod   string `json:"query_method"`
}
type SettlementBackend struct {
	Kind    SettlementKind   `json:"kind"`
	Polymer *PolymerSettings `json:"polymer,omitempty"`
}
type Settlement struct {
	Backends map[settlement.ID]SettlementBackend `json:"backends"`
}

type Config struct {
	Settlement          Settlement     `json:"settlement"`
	Storage             Storage        `json:"storage"`
	Providers           Providers      `json:"providers"`
	QuotePublisher      PublisherKind  `json:"quote_publisher,omitempty"`
	IntentAllowlist     []common.Hash  `json:"intent_allowlist,omitempty"`
	IntentSources       []IntentSource `json:"intent_sources"`
	Version             uint64         `json:"version"`
	Namespace           string         `json:"namespace"`
	Listen              string         `json:"listen"`
	ControlTokenEnv     string         `json:"control_token_env"`
	RequestsPerSecond   int            `json:"requests_per_second"`
	Workers             int            `json:"workers"`
	WorkIntervalSeconds int            `json:"work_interval_seconds"`
	Chains              []Chain        `json:"chains"`
	Signers             []Signer       `json:"signers"`
	Routes              []evm.Route    `json:"routes"`
	Development         bool           `json:"development,omitempty"`
}

var (
	envName        = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	identifierName = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
)

func Load(path string) (Config, error) {
	f, e := os.Open(path) // #nosec G304 -- The local operator explicitly selects the configuration file.
	if e != nil {
		return Config{}, errors.New("open configuration failed")
	}
	defer func() { _ = f.Close() }()
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
	if c.Version == 0 || !identifierName.MatchString(c.Namespace) {
		return errors.New("version and namespace required")
	}
	if c.Workers < 1 || c.Workers > 32 || c.WorkIntervalSeconds < 1 || c.WorkIntervalSeconds > 300 || c.RequestsPerSecond < 1 || c.RequestsPerSecond > 100 {
		return errors.New("invalid worker, polling, or rate bounds")
	}
	switch c.Storage.Kind {
	case RedisStorage:
		if !envName.MatchString(c.Storage.URLEnv) {
			return errors.New("redis requires a URL environment reference")
		}
	case MemoryStorage:
		if !c.Development || c.Storage.URLEnv != "" {
			return errors.New("memory storage requires development mode and no URL")
		}
	default:
		return errors.New("unknown storage backend")
	}
	if c.Providers.LIFI != nil && (c.Providers.LIFI.API == "" || !envName.MatchString(c.Providers.LIFI.KeyEnv)) {
		return errors.New("invalid LI.FI provider")
	}
	if c.QuotePublisher != "" && (c.QuotePublisher != LIFIPublisher || c.Providers.LIFI == nil) {
		return errors.New("quote publisher requires its configured provider")
	}
	for name, backend := range c.Settlement.Backends {
		if name == "" || !identifierName.MatchString(string(name)) {
			return errors.New("invalid settlement backend name")
		}
		switch backend.Kind {
		case PolymerSettlement:
			p := backend.Polymer
			if p == nil || p.API == "" || !envName.MatchString(p.KeyEnv) || p.RequestMethod == "" || p.QueryMethod == "" {
				return errors.New("invalid Polymer settlement configuration")
			}
		default:
			return errors.New("unsupported settlement backend kind")
		}
	}
	if !envName.MatchString(c.ControlTokenEnv) {
		return errors.New("invalid control secret environment reference")
	}
	if len(c.IntentAllowlist) > 1000 {
		return errors.New("order allowlist too large")
	}
	for _, id := range c.IntentAllowlist {
		if id == (common.Hash{}) {
			return errors.New("zero order ID in allowlist")
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
	sourceNames := map[string]bool{}
	hasLIFIStream := false
	if (!c.Development && len(c.IntentSources) == 0) || len(c.IntentSources) > 8 {
		return errors.New("configure one to eight intent sources")
	}
	for _, source := range c.IntentSources {
		if source.Name == "" || sourceNames[source.Name] {
			return errors.New("source name must be unique")
		}
		sourceNames[source.Name] = true
		switch source.Kind {
		case LIFIWebSocket:
			if hasLIFIStream {
				return errors.New("configure one LI.FI WebSocket source shared by all routes")
			}
			hasLIFIStream = true
			if c.Providers.LIFI == nil {
				return errors.New("LI.FI source requires a configured LI.FI provider")
			}
			if source.URL == "" || source.KeyEnv != "" && !envName.MatchString(source.KeyEnv) {
				return errors.New("invalid WebSocket source")
			}
		case EVMLogs:
			if !chains[source.ChainID] || source.Settler == (common.Address{}) || source.IntervalSeconds < 1 || source.IntervalSeconds > 60 || source.Lookback > 10000 {
				return errors.New("invalid log source")
			}
			found := false
			for _, route := range c.Routes {
				found = found || route.OriginChain == source.ChainID && route.InputSettler == source.Settler
			}
			if !found {
				return errors.New("log source must monitor a configured input settler")
			}
		default:
			return errors.New("unknown intent source kind")
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
		if _, ok := c.Settlement.Backends[r.Settlement]; !ok {
			return errors.New("route requires a configured settlement backend")
		}
		if r.InputDecimals > 36 || r.OutputDecimals != r.InputDecimals {
			return errors.New("fixed-reserve strategy requires equal configured token decimals in 0..36")
		}
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
	if len(c.Routes) == 0 && (!c.Development || len(c.IntentSources) > 0) {
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

func (c Config) AllowsIntent(id common.Hash) bool {
	if len(c.IntentAllowlist) == 0 {
		return true
	}
	for _, allowed := range c.IntentAllowlist {
		if allowed == id {
			return true
		}
	}
	return false
}
