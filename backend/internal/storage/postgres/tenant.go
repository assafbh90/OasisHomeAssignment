package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/assafbh/identityhub/internal/domain"
)

// PostgresTenantRepository reads/writes the tenants reference table. Tenants are
// the identity-bootstrap table (not under RLS): login resolves a tenant by slug
// before any tenant context exists.
type PostgresTenantRepository struct {
	db
}

// NewPostgresTenantRepository constructs the repository.
func NewPostgresTenantRepository(pool *pgxpool.Pool) *PostgresTenantRepository {
	return &PostgresTenantRepository{db{pool: pool}}
}

// FindBySlug looks up a tenant by its unique slug.
func (r *PostgresTenantRepository) FindBySlug(ctx context.Context, slug string) (domain.Tenant, error) {
	var t domain.Tenant
	err := r.pool.QueryRow(ctx,
		`SELECT id, slug, name, created_at, updated_at FROM tenants WHERE slug = $1`, slug,
	).Scan(&t.ID, &t.Slug, &t.Name, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		if isNoRows(err) {
			return domain.Tenant{}, domain.ErrTenantNotFound
		}
		return domain.Tenant{}, fmt.Errorf("find tenant by slug: %w", err)
	}
	return t, nil
}

// Create inserts a tenant (used by seeding/tests).
func (r *PostgresTenantRepository) Create(ctx context.Context, slug, name string) (domain.Tenant, error) {
	var t domain.Tenant
	err := r.pool.QueryRow(ctx,
		`INSERT INTO tenants (slug, name) VALUES ($1, $2)
		 RETURNING id, slug, name, created_at, updated_at`, slug, name,
	).Scan(&t.ID, &t.Slug, &t.Name, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return domain.Tenant{}, fmt.Errorf("create tenant: %w", err)
	}
	return t, nil
}
