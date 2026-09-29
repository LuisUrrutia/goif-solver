package app

import (
	"errors"
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
		endpoint, err := config.Secret(c.Storage.URLEnv)
		if err != nil {
			return nil, nil, err
		}
		options, err := redis.ParseURL(endpoint)
		if err != nil {
			return nil, nil, errors.New("invalid Redis URL")
		}
		options.DialTimeout = 5 * time.Second
		options.ReadTimeout = 5 * time.Second
		options.WriteTimeout = 5 * time.Second
		client := redis.NewClient(options)
		closeStore := func() { _ = client.Close() }
		store, err := redisstore.New(client, c.Namespace)
		if err != nil {
			closeStore()
			return nil, nil, err
		}
		return store, closeStore, nil
	default:
		return nil, nil, errors.New("unknown storage backend")
	}
}
