package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/assafbh/identityhub/internal/domain"
)

// PostgresApiTokenRepository is the tenant-scoped machine-API-key store. Only
// the SHA-256 hash of a key is stored — never the plaintext.
type PostgresApiTokenRepository struct {
	db
}

// NewPostgresApiTokenRepository constructs the repository.
func NewPostgresApiTokenRepository(pool *pgxpool.Pool) *PostgresApiTokenRepository {
	return &PostgresApiTokenRepository{db{pool: pool}}
}

const apiTokenCols = `id, tenant_id, owner_id, name, prefix, scopes, expires_at, last_used_at, revoked_at, created_at`

func scanToken(row pgx.Row) (domain.TokenMeta, error) {
	var t domain.TokenMeta
	err := row.Scan(&t.ID, &t.TenantID, &t.OwnerID, &t.Name, &t.Prefix, &t.Scopes,
		&t.ExpiresAt, &t.LastUsedAt, &t.RevokedAt, &t.CreatedAt)
	return t, err
}

// SaveToken inserts a new token row given its precomputed hash.
func (r *PostgresApiTokenRepository) SaveToken(ctx context.Context, meta *domain.TokenMeta, hash []byte) error {
	return r.inTenantTx(ctx, meta.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO api_tokens (tenant_id, owner_id, name, token_hash, prefix, scopes, expires_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)
			 RETURNING id, created_at`,
			meta.TenantID, meta.OwnerID, meta.Name, hash, meta.Prefix, meta.Scopes, meta.ExpiresAt,
		).Scan(&meta.ID, &meta.CreatedAt)
	})
}

// FindByHash looks up a token by its hash, regardless of tenant. This is the
// auth-bootstrap read (no tenant context exists yet), so it goes through the
// SECURITY DEFINER function find_api_token_by_hash, which bypasses RLS narrowly.
func (r *PostgresApiTokenRepository) FindByHash(ctx context.Context, hash []byte) (domain.TokenMeta, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+apiTokenCols+` FROM find_api_token_by_hash($1)`, hash)
	t, err := scanToken(row)
	if err != nil {
		if isNoRows(err) {
			return domain.TokenMeta{}, domain.ErrTokenNotFound
		}
		return domain.TokenMeta{}, fmt.Errorf("find token by hash: %w", err)
	}
	return t, nil
}

// ListByOwner lists a user's tokens (metadata only) within their tenant.
func (r *PostgresApiTokenRepository) ListByOwner(ctx context.Context, tenantID, ownerID uuid.UUID) ([]domain.TokenMeta, error) {
	var out []domain.TokenMeta
	err := r.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+apiTokenCols+` FROM api_tokens
			 WHERE tenant_id = $1 AND owner_id = $2 ORDER BY created_at DESC`,
			tenantID, ownerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanToken(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}
	return out, nil
}

// RevokeToken marks a token revoked and returns its hash so the caller can evict
// any cached validation. It returns ErrTokenNotFound if the token doesn't belong
// to ownerID in tenantID (or was already revoked).
func (r *PostgresApiTokenRepository) RevokeToken(ctx context.Context, tenantID, ownerID, tokenID uuid.UUID) ([]byte, error) {
	var hash []byte
	err := r.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`UPDATE api_tokens SET revoked_at = now()
			 WHERE tenant_id = $1 AND owner_id = $2 AND id = $3 AND revoked_at IS NULL
			 RETURNING token_hash`,
			tenantID, ownerID, tokenID).Scan(&hash)
	})
	if err != nil {
		if isNoRows(err) {
			return nil, domain.ErrTokenNotFound
		}
		return nil, fmt.Errorf("revoke token: %w", err)
	}
	return hash, nil
}

// TouchLastUsed records a token's most recent use.
func (r *PostgresApiTokenRepository) TouchLastUsed(ctx context.Context, tenantID, tokenID uuid.UUID) error {
	return r.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE api_tokens SET last_used_at = now() WHERE tenant_id = $1 AND id = $2`,
			tenantID, tokenID)
		return err
	})
}
