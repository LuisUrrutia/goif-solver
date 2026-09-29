// Package config validates deployment structure; selected adapters own their settings.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/LuisUrrutia/goif-solver/internal/intent"
	"github.com/LuisUrrutia/goif-solver/internal/settlement"
)

type (
	Kind       string
	Definition struct {
		Kind     Kind            `json:"kind"`
		Settings json.RawMessage `json:"settings"`
	}
)

type Route struct {
	Protocol intent.Kind `json:"protocol"`
	Name     string      `json:"name"`
}

func (r Route) Key() string { return string(r.Protocol) + "/" + r.Name }

type Provider struct {
	Definition
	Routes []Route `json:"routes"`
}
type Source struct {
	Name     string          `json:"name"`
	Provider string          `json:"provider,omitempty"`
	Protocol intent.Kind     `json:"protocol,omitempty"`
	Settings json.RawMessage `json:"settings"`
}
type API struct {
	Definition
	Routes []Route `json:"routes"`
}
type Publication struct {
	Provider string `json:"provider"`
	Route    Route  `json:"route"`
}
type StorageKind string

const (
	RedisStorage  StorageKind = "redis"
	MemoryStorage StorageKind = "memory"
)

type Storage struct {
	Kind            StorageKind `json:"kind"`
	URLEnv          string      `json:"url_env,omitempty"`
	PrimaryRunIDEnv string      `json:"primary_run_id_env,omitempty"`
}

type Config struct {
	Executions          map[intent.Kind]json.RawMessage `json:"executions"`
	Providers           map[string]Provider             `json:"providers"`
	Settlements         map[settlement.ID]Definition    `json:"settlements"`
	Storage             Storage                         `json:"storage"`
	ControlTokenEnv     string                          `json:"control_token_env"`
	Namespace           string                          `json:"namespace"`
	Listen              string                          `json:"listen"`
	Sources             []Source                        `json:"sources"`
	Publications        []Publication                   `json:"publications"`
	IntentAllowlist     []intent.Identity               `json:"intent_allowlist,omitempty"`
	APIs                []API                           `json:"apis,omitempty"`
	Version             uint64                          `json:"version"`
	RequestsPerSecond   int                             `json:"requests_per_second"`
	Workers             int                             `json:"workers"`
	WorkIntervalSeconds int                             `json:"work_interval_seconds"`
	Development         bool                            `json:"development,omitempty"`
}

const SchemaVersion uint64 = 9

var (
	envName        = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	identifierName = regexp.MustCompile(`^[A-Za-z0-9-]+$`)
)

func Decode[T any](raw json.RawMessage) (T, error) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, errors.New("invalid adapter settings")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return value, errors.New("trailing JSON data")
	}
	return value, nil
}

func Load(path string) (Config, error) {
	file, err := os.Open(path) // #nosec G304 -- The operator selects a local public configuration file.
	if err != nil {
		return Config{}, errors.New("open configuration failed")
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return Config{}, errors.New("configuration exceeds bounds")
	}
	c, err := Decode[Config](raw)
	if err != nil {
		return c, err
	}
	return c, c.Validate()
}

func (c Config) Validate() error {
	if c.Version != SchemaVersion || !identifierName.MatchString(c.Namespace) {
		return errors.New("configuration version 9 and namespace required")
	}
	if c.Workers < 1 || c.Workers > 32 || c.WorkIntervalSeconds < 1 || c.WorkIntervalSeconds > 300 || c.RequestsPerSecond < 1 || c.RequestsPerSecond > 100 {
		return errors.New("invalid operating bounds")
	}
	switch c.Storage.Kind {
	case RedisStorage:
		if !envName.MatchString(c.Storage.URLEnv) {
			return errors.New("redis requires a URL environment reference")
		}
		if !ValidEnv(c.Storage.PrimaryRunIDEnv) {
			return errors.New("persistent Redis requires an expected primary identity environment reference")
		}
	case MemoryStorage:
		if !c.Development || c.Storage.URLEnv != "" || c.Storage.PrimaryRunIDEnv != "" {
			return errors.New("memory storage requires development mode and no URL")
		}
	default:
		return errors.New("unknown storage backend")
	}
	if !envName.MatchString(c.ControlTokenEnv) {
		return errors.New("invalid control secret reference")
	}
	if len(c.APIs) > 4 || len(c.Executions) > 16 || len(c.Sources) > 32 || len(c.Publications) > 64 || len(c.IntentAllowlist) > 1000 {
		return errors.New("deployment exceeds bounds")
	}
	for kind, settings := range c.Executions {
		if (intent.Identity{Kind: kind, NativeID: "validation"}).Validate() != nil || !json.Valid(settings) {
			return errors.New("invalid execution definition")
		}
	}
	for _, api := range c.APIs {
		if api.Kind == "" || !json.Valid(api.Settings) || len(api.Routes) == 0 || len(api.Routes) > 64 {
			return errors.New("invalid API definition")
		}
		seen := map[string]bool{}
		for _, route := range api.Routes {
			if !identifierName.MatchString(route.Name) || c.Executions[route.Protocol] == nil || seen[route.Key()] {
				return errors.New("invalid API route binding")
			}
			seen[route.Key()] = true
		}
	}
	for _, id := range c.IntentAllowlist {
		if id.Validate() != nil || c.Executions[id.Kind] == nil {
			return errors.New("allowlist requires a configured protocol and native identifier")
		}
	}
	for name, provider := range c.Providers {
		if !identifierName.MatchString(name) || provider.Kind == "" || !json.Valid(provider.Settings) || len(provider.Routes) == 0 {
			return errors.New("invalid provider definition")
		}
		seen := map[string]bool{}
		for _, route := range provider.Routes {
			if !identifierName.MatchString(route.Name) || c.Executions[route.Protocol] == nil || seen[route.Key()] {
				return errors.New("invalid or duplicate provider route")
			}
			seen[route.Key()] = true
		}
	}
	for name, backend := range c.Settlements {
		if !identifierName.MatchString(string(name)) || backend.Kind == "" || !json.Valid(backend.Settings) {
			return errors.New("invalid settlement definition")
		}
	}
	names, streams := map[string]bool{}, map[string]bool{}
	for _, source := range c.Sources {
		if !identifierName.MatchString(source.Name) || names[source.Name] || !json.Valid(source.Settings) || (source.Provider == "") == (source.Protocol == "") {
			return errors.New("source requires a unique name and exactly one provider or protocol")
		}
		names[source.Name] = true
		if source.Provider != "" {
			if _, ok := c.Providers[source.Provider]; !ok || streams[source.Provider] {
				return errors.New("configure one shared stream per selected provider")
			}
			streams[source.Provider] = true
		} else if c.Executions[source.Protocol] == nil {
			return errors.New("unknown source protocol")
		}
	}
	publications := map[string]bool{}
	for _, publication := range c.Publications {
		provider, ok := c.Providers[publication.Provider]
		found := false
		for _, route := range provider.Routes {
			found = found || route == publication.Route
		}
		key := publication.Provider + "/" + publication.Route.Key()
		if !ok || !found || publications[key] {
			return errors.New("publication requires a unique bound provider route")
		}
		publications[key] = true
	}
	if !c.Development && (len(c.Executions) == 0 || len(c.Sources) == 0 && len(c.APIs) == 0) {
		return errors.New("configure execution and intent sources")
	}
	return nil
}

func Secret(name string) (string, error) {
	if !envName.MatchString(name) {
		return "", errors.New("invalid environment reference")
	}
	value := os.Getenv(name)
	if value == "" {
		return "", fmt.Errorf("required environment variable %s is unset", name)
	}
	return value, nil
}
func ValidEnv(name string) bool { return envName.MatchString(name) }

func (c Config) AllowsIntent(id intent.Identity) bool {
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
