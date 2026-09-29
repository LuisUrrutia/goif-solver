package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/LuisUrrutia/goif-solver/internal/config"
	"github.com/LuisUrrutia/goif-solver/internal/coordination"
	"github.com/LuisUrrutia/goif-solver/internal/storage/memorystore"
	"github.com/LuisUrrutia/goif-solver/internal/storage/redisstore"
	"github.com/redis/go-redis/v9"
)

func OpenStore(c config.Config) (coordination.Backend, func(), error) {
	switch c.Storage.Kind {
	case config.MemoryStorage:
		return memorystore.New(), func() {}, nil
	case config.RedisStorage:
		options, err := redisOptions(c.Storage)
		if err != nil {
			return nil, nil, err
		}
		identity, err := config.Secret(c.Storage.PrimaryRunIDEnv)
		if err != nil {
			return nil, nil, err
		}
		return redisstore.Open(options, c.Namespace, identity)
	default:
		return nil, nil, errors.New("unknown storage backend")
	}
}

func redisOptions(storage config.Storage) (*redis.Options, error) {
	endpoint, err := config.Secret(storage.URLEnv)
	if err != nil {
		return nil, err
	}
	options, err := redis.ParseURL(endpoint)
	if err != nil {
		return nil, errors.New("invalid Redis URL")
	}
	options.DialTimeout = 5 * time.Second
	options.ReadTimeout = 5 * time.Second
	options.WriteTimeout = 5 * time.Second
	options.ContextTimeoutEnabled = true
	return options, nil
}

type StorageReport struct {
	PrimaryRunID string `json:"primary_run_id"`
	Approved     bool   `json:"approved"`
}

func InspectStorage(ctx context.Context, c config.Config) (StorageReport, error) {
	if c.Storage.Kind != config.RedisStorage {
		return StorageReport{}, errors.New("primary inspection requires Redis storage")
	}
	options, err := redisOptions(c.Storage)
	if err != nil {
		return StorageReport{}, err
	}
	identity, err := redisstore.InspectPrimary(ctx, options)
	if err != nil {
		return StorageReport{}, err
	}
	return StorageReport{PrimaryRunID: identity, Approved: strings.EqualFold(os.Getenv(c.Storage.PrimaryRunIDEnv), identity)}, nil
}
