package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// RedisRateLimiter is a fixed-window rate limiter keyed at `ratelimit:{key}`.
// Simple and adequate for login/oauth throttling; the window resets atomically
// via INCR + EXPIRE.
type RedisRateLimiter struct {
	client *goredis.Client
	max    int
	window time.Duration
}

// NewRedisRateLimiter constructs the limiter allowing maxAttempts per window.
func NewRedisRateLimiter(client *goredis.Client, maxAttempts int, window time.Duration) *RedisRateLimiter {
	return &RedisRateLimiter{client: client, max: maxAttempts, window: window}
}

// AllowAttempt records an attempt for key and reports whether it is allowed. When
// blocked, retryAfter is the time until the window resets.
const rateLimitKeyPrefix = "ratelimit:"

func (r *RedisRateLimiter) AllowAttempt(ctx context.Context, key string) (bool, time.Duration, error) {
	k := rateLimitKeyPrefix + key
	count, err := r.client.Incr(ctx, k).Result()
	if err != nil {
		return false, 0, fmt.Errorf("ratelimit incr: %w", err)
	}
	if count == 1 {
		if err := r.client.Expire(ctx, k, r.window).Err(); err != nil {
			return false, 0, fmt.Errorf("ratelimit expire: %w", err)
		}
	}
	if count > int64(r.max) {
		ttl, err := r.client.TTL(ctx, k).Result()
		if err != nil || ttl < 0 {
			ttl = r.window
		}
		return false, ttl, nil
	}
	return true, 0, nil
}
