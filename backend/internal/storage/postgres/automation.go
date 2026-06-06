package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/assafbh/identityhub/internal/domain"
)

// PostgresAutomationRepository is the tenant-scoped store of automations. CRUD
// goes through the RLS path (inTenantTx); the cross-tenant ClaimDue uses the
// SECURITY DEFINER claim function.
type PostgresAutomationRepository struct {
	db
}

// NewPostgresAutomationRepository constructs the repository.
func NewPostgresAutomationRepository(pool *pgxpool.Pool) *PostgresAutomationRepository {
	return &PostgresAutomationRepository{db{pool: pool}}
}

const automationColumns = `id, tenant_id, owner_user_id, name, site_url, provider,
	project_key, interval_seconds, enabled, status, next_scan_at, locked_at,
	last_run_at, last_error, created_at, updated_at`

// rowScanner is satisfied by both pgx.Row and pgx.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanAutomation(s rowScanner) (domain.Automation, error) {
	var (
		a            domain.Automation
		intervalSecs int
		status       string
		lockedAt     *time.Time
		lastRunAt    *time.Time
		lastErr      *string
	)
	if err := s.Scan(&a.ID, &a.TenantID, &a.OwnerUserID, &a.Name, &a.SiteURL, &a.Provider,
		&a.ProjectKey, &intervalSecs, &a.Enabled, &status, &a.NextScanAt, &lockedAt,
		&lastRunAt, &lastErr, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return domain.Automation{}, err
	}
	a.Interval = time.Duration(intervalSecs) * time.Second
	a.Status = domain.AutomationStatus(status)
	a.LockedAt = lockedAt
	a.LastRunAt = lastRunAt
	if lastErr != nil {
		a.LastError = *lastErr
	}
	return a, nil
}

// Create inserts a new automation and fills generated fields.
func (r *PostgresAutomationRepository) Create(ctx context.Context, a *domain.Automation) error {
	return r.inTenantTx(ctx, a.TenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`INSERT INTO automations
			   (tenant_id, owner_user_id, name, site_url, provider, project_key, interval_seconds, enabled, next_scan_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
			 RETURNING `+automationColumns,
			a.TenantID, a.OwnerUserID, a.Name, a.SiteURL, a.Provider, a.ProjectKey,
			int(a.Interval.Seconds()), a.Enabled, a.NextScanAt)
		got, err := scanAutomation(row)
		if err != nil {
			return err
		}
		*a = got
		return nil
	})
}

// Get returns one automation by id within the tenant.
func (r *PostgresAutomationRepository) Get(ctx context.Context, tenantID, id uuid.UUID) (domain.Automation, error) {
	var out domain.Automation
	err := r.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT `+automationColumns+` FROM automations WHERE id = $1 AND tenant_id = $2`, id, tenantID)
		got, err := scanAutomation(row)
		if err != nil {
			if isNoRows(err) {
				return domain.ErrAutomationNotFound
			}
			return err
		}
		out = got
		return nil
	})
	if err != nil {
		return domain.Automation{}, err
	}
	return out, nil
}

// ListByTenant returns all automations for a tenant, newest first.
func (r *PostgresAutomationRepository) ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]domain.Automation, error) {
	var out []domain.Automation
	err := r.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+automationColumns+` FROM automations WHERE tenant_id = $1 ORDER BY created_at DESC`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAutomation(rows)
			if err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("list automations: %w", err)
	}
	return out, nil
}

// Update mutates editable fields and returns the updated row.
func (r *PostgresAutomationRepository) Update(ctx context.Context, a *domain.Automation) error {
	return r.inTenantTx(ctx, a.TenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`UPDATE automations
			   SET name=$1, site_url=$2, project_key=$3, interval_seconds=$4, enabled=$5, updated_at=now()
			 WHERE id=$6 AND tenant_id=$7
			 RETURNING `+automationColumns,
			a.Name, a.SiteURL, a.ProjectKey, int(a.Interval.Seconds()), a.Enabled, a.ID, a.TenantID)
		got, err := scanAutomation(row)
		if err != nil {
			if isNoRows(err) {
				return domain.ErrAutomationNotFound
			}
			return err
		}
		*a = got
		return nil
	})
}

// Delete removes an automation (idempotent: not-found is not an error here so the
// caller can also clear the seen-set without racing).
func (r *PostgresAutomationRepository) Delete(ctx context.Context, tenantID, id uuid.UUID) error {
	return r.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM automations WHERE id = $1 AND tenant_id = $2`, id, tenantID)
		return err
	})
}

// SetDue forces an automation's next_scan_at (used by "run now").
func (r *PostgresAutomationRepository) SetDue(ctx context.Context, tenantID, id uuid.UUID, at time.Time) error {
	return r.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `UPDATE automations SET next_scan_at = $1, updated_at = now() WHERE id = $2 AND tenant_id = $3`, at, id, tenantID)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return domain.ErrAutomationNotFound
		}
		return nil
	})
}

// ClaimDue atomically claims up to batch due automations across all tenants. It
// uses the SECURITY DEFINER claim function (no tenant GUC: it is intentionally
// cross-tenant), then marks the rows running.
func (r *PostgresAutomationRepository) ClaimDue(ctx context.Context, batch int, lease time.Duration) ([]domain.Automation, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+automationColumns+` FROM claim_due_automations($1, make_interval(secs => $2))`,
		batch, int(lease.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("claim due automations: %w", err)
	}
	defer rows.Close()
	var out []domain.Automation
	for rows.Next() {
		a, err := scanAutomation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Complete releases a claimed automation: clears the lock, records the result,
// and schedules the next scan. Tenant-scoped (the row's tenant is known).
func (r *PostgresAutomationRepository) Complete(ctx context.Context, tenantID, id uuid.UUID, nextScanAt time.Time, runErr string) error {
	return r.inTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var errPtr *string
		if runErr != "" {
			errPtr = &runErr
		}
		_, err := tx.Exec(ctx,
			`UPDATE automations
			   SET status='idle', locked_at=NULL, last_run_at=now(), next_scan_at=$1, last_error=$2, updated_at=now()
			 WHERE id=$3 AND tenant_id=$4`,
			nextScanAt, errPtr, id, tenantID)
		return err
	})
}
