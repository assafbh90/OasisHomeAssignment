package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/assafbh/identityhub/internal/domain"
)

// PostgresUserRepository is the tenant-scoped user store.
type PostgresUserRepository struct {
	db
}

// NewPostgresUserRepository constructs the repository.
func NewPostgresUserRepository(pool *pgxpool.Pool) *PostgresUserRepository {
	return &PostgresUserRepository{db{pool: pool}}
}

// FindUserForLogin looks up a user by email across all tenants. This is the
// auth-bootstrap read (no tenant context exists yet), so it goes through the
// SECURITY DEFINER function find_user_for_login, which bypasses RLS. Email is
// globally unique, so at most one row matches.
func (r *PostgresUserRepository) FindUserForLogin(ctx context.Context, email string) (domain.User, error) {
	var u domain.User
	err := r.pool.QueryRow(ctx,
		`SELECT id, tenant_id, email, password_hash, status, created_at, updated_at
		 FROM find_user_for_login($1)`, email,
	).Scan(&u.ID, &u.TenantID, &u.Email, &u.PasswordHash, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if isNoRows(err) {
			return domain.User{}, domain.ErrUserNotFound
		}
		return domain.User{}, fmt.Errorf("find user for login: %w", err)
	}
	return u, nil
}

// FindUserByEmail returns the active-or-not user with email in tenantID. Runs
// under the tenant GUC so RLS applies. Used for tenant-scoped existence checks
// (e.g. seeding), not for login.
func (r *PostgresUserRepository) FindUserByEmail(ctx context.Context, tenantID uuid.UUID, email string) (domain.User, error) {
	var u domain.User
	err := r.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id, tenant_id, email, password_hash, status, created_at, updated_at
			 FROM users WHERE tenant_id = $1 AND email = $2`, tenantID, email,
		).Scan(&u.ID, &u.TenantID, &u.Email, &u.PasswordHash, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	})
	if err != nil {
		if isNoRows(err) {
			return domain.User{}, domain.ErrUserNotFound
		}
		return domain.User{}, fmt.Errorf("find user by email: %w", err)
	}
	return u, nil
}

// CreateUser inserts a user (used by seeding/tests).
func (r *PostgresUserRepository) CreateUser(ctx context.Context, tenantID uuid.UUID, email, passwordHash string) (domain.User, error) {
	var u domain.User
	err := r.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO users (tenant_id, email, password_hash)
			 VALUES ($1, $2, $3)
			 RETURNING id, tenant_id, email, password_hash, status, created_at, updated_at`,
			tenantID, email, passwordHash,
		).Scan(&u.ID, &u.TenantID, &u.Email, &u.PasswordHash, &u.Status, &u.CreatedAt, &u.UpdatedAt)
	})
	if err != nil {
		return domain.User{}, fmt.Errorf("create user: %w", err)
	}
	return u, nil
}
