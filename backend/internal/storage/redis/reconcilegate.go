package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"

	"github.com/assafbh/identityhub/internal/secret"
)

const (
	reconcileLastPrefix = "reconcile:last:"
	reconcileLockPrefix = "reconcile:lock:"
	// reconcileLockTTL caps how long a single-flight reconcile may hold the lock
	// (a crashed holder is reaped when it expires).
	reconcileLockTTL = 2 * time.Minute
)

// RedisReconcileGate throttles + single-flights per-tenant reconciliation, so a
// burst of simultaneous connects collapses to at most one reconcile: a throttle
// window (reconcile:last:{tenant}) and a single-flight lock (reconcile:lock:{tenant}).
type RedisReconcileGate struct {
	client *goredis.Client
	window time.Duration
}

// NewRedisReconcileGate constructs the gate with the throttle window (the minimum
// interval between reconciles for a tenant when not forced).
func NewRedisReconcileGate(client *goredis.Client, window time.Duration) *RedisReconcileGate {
	return &RedisReconcileGate{client: client, window: window}
}

// Begin reports whether the caller should reconcile now. When force is false it
// skips if a reconcile happened within the window. It then takes a single-flight
// lock; only one concurrent caller proceeds. finish() stamps the throttle and
// releases the lock; when proceed is false, finish is a no-op.
func (g *RedisReconcileGate) Begin(ctx context.Context, tenantID uuid.UUID, force bool) (bool, func(), error) {
	noop := func() {}
	lastKey := reconcileLastPrefix + tenantID.String()
	lockKey := reconcileLockPrefix + tenantID.String()

	if !force {
		recent, err := g.client.Exists(ctx, lastKey).Result()
		if err != nil {
			return false, noop, fmt.Errorf("reconcile throttle check: %w", err)
		}
		if recent > 0 {
			return false, noop, nil // reconciled within the window
		}
	}

	token, err := secret.NewToken(secret.TokenBytes)
	if err != nil {
		return false, noop, err
	}
	acquired, err := g.client.SetNX(ctx, lockKey, token, reconcileLockTTL).Result()
	if err != nil {
		return false, noop, fmt.Errorf("reconcile lock: %w", err)
	}
	if !acquired {
		return false, noop, nil // another reconcile is in flight
	}

	finish := func() {
		_ = g.client.Set(ctx, lastKey, "1", g.window).Err()
		_ = g.client.Del(ctx, lockKey).Err()
	}
	return true, finish, nil
}
