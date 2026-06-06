// Package postgres holds the Postgres-backed repository adapters. Every method
// is tenant-scoped: it both filters by tenant_id in SQL (layer 2) and runs
// inside a transaction that sets the `app.tenant_id` GUC so Row-Level Security
// (layer 3) applies. Adapters import only the domain package and pgx.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TokenCipher encrypts/decrypts provider tokens at rest. It is consumer-defined
// here (the credential repository owns the at-rest encryption); the concrete
// AES-256-GCM implementation lives in internal/integration/oauthtoken.
type TokenCipher interface {
	EncryptToken(plaintext string) ([]byte, error)
	DecryptToken(ciphertext []byte) (string, error)
}

// db is the shared base for repositories.
type db struct {
	pool *pgxpool.Pool
}

// inTenantTx runs fn inside a transaction with app.tenant_id set to tenantID,
// so RLS scopes every statement fn issues. The setting uses set_config(...,
// true) — transaction-local — so it never leaks to the next user of a pooled
// connection.
func (d db) inTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(pgx.Tx) error) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after Commit

	if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID.String()); err != nil {
		return fmt.Errorf("set tenant guc: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

// isNoRows reports whether err is pgx's no-rows sentinel.
func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
