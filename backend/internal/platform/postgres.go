package platform

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/assafbh/identityhub/internal/config"
)

const (
	maxConnIdleTime   = 5 * time.Minute
	healthCheckPeriod = 30 * time.Second
	pgPingTimeout     = 5 * time.Second
)

// NewPostgresPool builds and verifies a pgx connection pool. Every pooled
// connection gets session GUCs that bound query/transaction duration — these
// are the safety net that reaps a FOR-UPDATE refresh lock held by a crashed
// instance, so no lock is ever "held forever".
func NewPostgresPool(ctx context.Context, cfg config.PostgresConfig) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("parse postgres dsn: %w", err)
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MaxConnIdleTime = maxConnIdleTime
	poolCfg.HealthCheckPeriod = healthCheckPeriod

	runtimeParams := poolCfg.ConnConfig.RuntimeParams
	if cfg.StatementTimeout > 0 {
		runtimeParams["statement_timeout"] = msString(cfg.StatementTimeout)
	}
	if cfg.IdleInTxTimeout > 0 {
		runtimeParams["idle_in_transaction_session_timeout"] = msString(cfg.IdleInTxTimeout)
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, pgPingTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return pool, nil
}

func msString(d time.Duration) string {
	return fmt.Sprintf("%d", d.Milliseconds())
}
