package platform

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/assafbh/identityhub/internal/config"
)

const (
	redisDialTimeout = 5 * time.Second
	redisRWTimeout   = 3 * time.Second
	redisPingTimeout = 5 * time.Second
)

// NewRedisClient builds and verifies a Redis client.
func NewRedisClient(ctx context.Context, cfg config.RedisConfig) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         cfg.Addr,
		Password:     cfg.Password,
		DB:           cfg.DB,
		DialTimeout:  redisDialTimeout,
		ReadTimeout:  redisRWTimeout,
		WriteTimeout: redisRWTimeout,
	})

	pingCtx, cancel := context.WithTimeout(ctx, redisPingTimeout)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return client, nil
}

// RedisPinger adapts *redis.Client to a simple Ping(ctx) error health check.
type RedisPinger struct{ Client *redis.Client }

// Ping reports Redis reachability.
func (p RedisPinger) Ping(ctx context.Context) error { return p.Client.Ping(ctx).Err() }
