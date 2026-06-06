package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/go-redis/redis_rate/v10"
	goredis "github.com/redis/go-redis/v9"
)

const rateLimitKeyPrefix = "ratelimit:"

// RedisRateLimiter throttles attempts per key using the maintained redis_rate
// limiter (a GCRA token bucket), which avoids the burst-at-window-boundary flaw
// of a naive fixed window and runs atomically in Redis.
type RedisRateLimiter struct {
	limiter *redis_rate.Limiter
	limit   redis_rate.Limit
}

// NewRedisRateLimiter constructs the limiter allowing maxAttempts per window.
func NewRedisRateLimiter(client *goredis.Client, maxAttempts int, window time.Duration) *RedisRateLimiter {
	return &RedisRateLimiter{
		limiter: redis_rate.NewLimiter(client),
		limit:   redis_rate.Limit{Rate: maxAttempts, Burst: maxAttempts, Period: window},
	}
}

// AllowAttempt records an attempt for key and reports whether it is allowed. When
// blocked, retryAfter is the time until the next attempt is permitted.
func (r *RedisRateLimiter) AllowAttempt(ctx context.Context, key string) (bool, time.Duration, error) {
	res, err := r.limiter.Allow(ctx, rateLimitKeyPrefix+key, r.limit)
	if err != nil {
		return false, 0, fmt.Errorf("ratelimit allow: %w", err)
	}
	if res.Allowed > 0 {
		return true, 0, nil
	}
	return false, res.RetryAfter, nil
}
