package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/assafbh/identityhub/internal/domain"
)

// PostgresTicketRepository is the tenant-scoped store of app-created tickets,
// backing the "recent tickets" view.
type PostgresTicketRepository struct {
	db
}

// NewPostgresTicketRepository constructs the repository.
func NewPostgresTicketRepository(pool *pgxpool.Pool) *PostgresTicketRepository {
	return &PostgresTicketRepository{db{pool: pool}}
}

// SaveTicket records a ticket created through the app.
func (r *PostgresTicketRepository) SaveTicket(ctx context.Context, t *domain.CreatedTicket) error {
	return r.inTenantTx(ctx, t.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO created_tickets (tenant_id, user_id, provider, project_key, issue_key, issue_url, title)
			 VALUES ($1, $2, $3, $4, $5, $6, $7)
			 RETURNING id, created_at`,
			t.TenantID, t.UserID, t.Provider, t.ProjectKey, t.IssueKey, t.IssueURL, t.Title,
		).Scan(&t.ID, &t.CreatedAt)
	})
}

// ListRecentByProject returns the most recent app-created tickets for a project,
// scoped to the tenant user, newest first.
func (r *PostgresTicketRepository) ListRecentByProject(ctx context.Context, tenantID, userID uuid.UUID, projectKey string, limit int) ([]domain.CreatedTicket, error) {
	var out []domain.CreatedTicket
	err := r.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, tenant_id, user_id, provider, project_key, issue_key, issue_url, title, created_at
			 FROM created_tickets
			 WHERE tenant_id = $1 AND user_id = $2 AND project_key = $3
			 ORDER BY created_at DESC LIMIT $4`,
			tenantID, userID, projectKey, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var t domain.CreatedTicket
			if err := rows.Scan(&t.ID, &t.TenantID, &t.UserID, &t.Provider, &t.ProjectKey,
				&t.IssueKey, &t.IssueURL, &t.Title, &t.CreatedAt); err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list recent tickets: %w", err)
	}
	return out, nil
}
