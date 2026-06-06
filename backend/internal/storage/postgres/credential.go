package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/assafbh/identityhub/internal/domain"
)

// PostgresCredentialRepository is the tenant-scoped store for integration
// credentials. It owns at-rest encryption: token fields are AES-256-GCM
// encrypted via the injected TokenCipher before persistence and decrypted on
// load, so the rest of the system works with plaintext domain.Credential.
type PostgresCredentialRepository struct {
	db
	cipher TokenCipher
}

// NewPostgresCredentialRepository constructs the repository.
func NewPostgresCredentialRepository(pool *pgxpool.Pool, cipher TokenCipher) *PostgresCredentialRepository {
	return &PostgresCredentialRepository{db: db{pool: pool}, cipher: cipher}
}

const credentialCols = `id, tenant_id, user_id, provider, access_token, refresh_token,
	scopes, external_account_id, site_url, access_expires_at, refresh_last_used_at, status, created_at, updated_at`

func (r *PostgresCredentialRepository) scanCredential(row pgx.Row) (*domain.Credential, error) {
	var (
		c                     domain.Credential
		accessEnc, refreshEnc []byte
		externalID, siteURL   *string
	)
	if err := row.Scan(&c.ID, &c.TenantID, &c.UserID, &c.Provider, &accessEnc, &refreshEnc,
		&c.Scopes, &externalID, &siteURL, &c.AccessExpiresAt, &c.RefreshLastUsedAt, &c.Status,
		&c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	access, err := r.cipher.DecryptToken(accessEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt access token: %w", err)
	}
	refresh, err := r.cipher.DecryptToken(refreshEnc)
	if err != nil {
		return nil, fmt.Errorf("decrypt refresh token: %w", err)
	}
	c.AccessToken = access
	c.RefreshToken = refresh
	if externalID != nil {
		c.ExternalAccountID = *externalID
	}
	if siteURL != nil {
		c.SiteURL = *siteURL
	}
	return &c, nil
}

// SaveCredential upserts a credential (a reconnect replaces the prior one).
func (r *PostgresCredentialRepository) SaveCredential(ctx context.Context, c *domain.Credential) error {
	accessEnc, err := r.cipher.EncryptToken(c.AccessToken)
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}
	refreshEnc, err := r.cipher.EncryptToken(c.RefreshToken)
	if err != nil {
		return fmt.Errorf("encrypt refresh token: %w", err)
	}
	return r.inTenantTx(ctx, c.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO integration_credentials
			   (tenant_id, user_id, provider, access_token, refresh_token, scopes,
			    external_account_id, site_url, access_expires_at, refresh_last_used_at, status)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			 ON CONFLICT (tenant_id, user_id, provider) DO UPDATE SET
			   access_token = EXCLUDED.access_token,
			   refresh_token = EXCLUDED.refresh_token,
			   scopes = EXCLUDED.scopes,
			   external_account_id = EXCLUDED.external_account_id,
			   site_url = EXCLUDED.site_url,
			   access_expires_at = EXCLUDED.access_expires_at,
			   refresh_last_used_at = EXCLUDED.refresh_last_used_at,
			   status = EXCLUDED.status,
			   updated_at = now()
			 RETURNING id, created_at, updated_at`,
			c.TenantID, c.UserID, c.Provider, accessEnc, refreshEnc, c.Scopes,
			nullString(c.ExternalAccountID), nullString(c.SiteURL), c.AccessExpiresAt, c.RefreshLastUsedAt, c.Status,
		).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	})
}

// LoadCredential returns the tenant user's credential for a provider.
func (r *PostgresCredentialRepository) LoadCredential(ctx context.Context, tenantID, userID uuid.UUID, provider string) (*domain.Credential, error) {
	var c *domain.Credential
	err := r.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT `+credentialCols+` FROM integration_credentials
			 WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`,
			tenantID, userID, provider)
		var err error
		c, err = r.scanCredential(row)
		return err
	})
	if err != nil {
		if isNoRows(err) {
			return nil, domain.ErrCredentialNotFound
		}
		return nil, fmt.Errorf("load credential: %w", err)
	}
	return c, nil
}

// UpdateTokens persists rotated tokens + new expiry + refresh_last_used_at in a
// single statement.
func (r *PostgresCredentialRepository) UpdateTokens(ctx context.Context, c *domain.Credential) error {
	return r.inTenantTx(ctx, c.TenantID, func(tx pgx.Tx) error {
		return r.updateTokensTx(ctx, tx, c)
	})
}

func (r *PostgresCredentialRepository) updateTokensTx(ctx context.Context, tx pgx.Tx, c *domain.Credential) error {
	accessEnc, err := r.cipher.EncryptToken(c.AccessToken)
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}
	refreshEnc, err := r.cipher.EncryptToken(c.RefreshToken)
	if err != nil {
		return fmt.Errorf("encrypt refresh token: %w", err)
	}
	_, err = tx.Exec(ctx,
		`UPDATE integration_credentials SET
		   access_token = $4, refresh_token = $5, access_expires_at = $6,
		   refresh_last_used_at = $7, scopes = $8, status = $9, updated_at = now()
		 WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`,
		c.TenantID, c.UserID, c.Provider, accessEnc, refreshEnc,
		c.AccessExpiresAt, c.RefreshLastUsedAt, c.Scopes, c.Status)
	return err
}

// MarkNeedsReauth flips a credential's status to needs_reauth.
func (r *PostgresCredentialRepository) MarkNeedsReauth(ctx context.Context, tenantID, userID uuid.UUID, provider string) error {
	return r.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`UPDATE integration_credentials SET status = 'needs_reauth', updated_at = now()
			 WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`,
			tenantID, userID, provider)
		return err
	})
}

// DeleteCredential removes a credential (disconnect).
func (r *PostgresCredentialRepository) DeleteCredential(ctx context.Context, tenantID, userID uuid.UUID, provider string) error {
	return r.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`DELETE FROM integration_credentials
			 WHERE tenant_id = $1 AND user_id = $2 AND provider = $3`,
			tenantID, userID, provider)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrCredentialNotFound
		}
		return nil
	})
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
