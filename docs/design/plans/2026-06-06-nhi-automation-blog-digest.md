# NHI Automation (Blog Digest) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a generalized **Automation** subsystem: a tenant member watches a blog/site URL; a separate scheduler container periodically discovers new posts via `sitemap.xml`, scrapes each to markdown, summarizes it with a local Ollama model, and files a Jira ticket per post into a chosen project.

**Architecture:** New `internal/automation` bounded context (consumer-defined ports: `Discoverer`, `Scraper`, `Summarizer`, `TicketCreator`, `SeenSet`, `Repository`) plus three thin adapters (sitemap discovery, fetch→readability→markdown scrape, Ollama HTTP client). A `scheduler` subcommand of the existing binary claims due automations with a Postgres `SELECT … FOR UPDATE SKIP LOCKED` lease and runs the pipeline serially (one Ollama call at a time → bounded memory). Reuses the existing Jira client, reactive token manager, `ticketreport.CreateTicket`, RLS, and config patterns unchanged.

**Tech Stack:** Go 1.25, gin, pgx/v5, go-redis/v9, viper, testify, testcontainers; new deps `github.com/go-shiori/go-readability` + `github.com/JohannesKaufmann/html-to-markdown/v2`; Docker Compose adds memory-limited `ollama` (custom image, pre-pulled `qwen2.5:0.5b`) and `scheduler` services; React/Vite SPA gains an Automation tab.

**Module path:** `github.com/assafbh/identityhub`. Backend root: `backend/`. Run Go commands with `go -C backend …` (or `cd backend`).

**Conventions to follow (from the existing codebase):**
- Repos embed `db` and run every statement inside `inTenantTx(ctx, tenantID, fn)` (sets `app.tenant_id` GUC for RLS). The ONE cross-tenant query (claim) goes through a `SECURITY DEFINER` SQL function, exactly like `find_user_for_login`.
- Consumer-defined interfaces live in the package that uses them; concretes are wired only in `internal/app`.
- Errors wrap sentinels from `internal/domain` with `%w`; transport maps them in `respondError`.
- Tests are table-driven, `t.Parallel()`, AAA, `require`. Integration tests use the `//go:build integration` tag and the shared pools in `test/integration/harness_test.go`.

---

## File Structure

**Create:**
- `backend/internal/domain/automation.go` — `Automation` entity + `AutomationStatus`.
- `backend/internal/automation/service.go` — CRUD use-cases + `RunOnce` orchestration + ports.
- `backend/internal/automation/scheduler.go` — claim→run→complete loop.
- `backend/internal/automation/service_test.go`, `scheduler_test.go` — unit tests (fakes).
- `backend/internal/automation/discover/sitemap.go` (+ `sitemap_test.go`) — sitemap discovery adapter.
- `backend/internal/automation/scrape/scrape.go` (+ `scrape_test.go`) — fetch→readability→markdown adapter.
- `backend/internal/automation/summarize/ollama.go` (+ `ollama_test.go`) — Ollama HTTP client.
- `backend/internal/storage/postgres/automation.go` — `PostgresAutomationRepository`.
- `backend/internal/storage/redis/automationseen.go` — `RedisAutomationSeenSet`.
- `backend/internal/transport/http/automation_handler.go` — CRUD + run-now handlers.
- `backend/migrations/000002_automations.up.sql` / `.down.sql`.
- `backend/test/integration/automation_test.go` — repo + e2e integration tests.
- `deployments/docker/ollama.Dockerfile` — pre-warmed Ollama image.
- `frontend/src/components/AutomationPanel.tsx` — the Automation tab UI.

**Modify:**
- `backend/internal/config/{keys.go,config.go,config_test.go}` — new config blocks.
- `backend/internal/transport/http/{dto.go,router.go}` — automation DTOs + routes.
- `backend/internal/app/app.go` — wire automation; add `RunScheduler`.
- `backend/internal/app/scheduler.go` (new file in `app`) — `App.RunScheduler`.
- `backend/cmd/api/main.go` is unchanged; `internal/app/app.go` `Run()` dispatches the `scheduler` subcommand.
- `docker-compose.yml`, `.env.example`, `README.md`, `Taskfile.yml`.
- `frontend/src/{types.ts,api.ts}` and `frontend/src/components/Dashboard.tsx`.

---

## Task 1: Config — automation, ollama, scheduler blocks

**Files:**
- Modify: `backend/internal/config/keys.go`
- Modify: `backend/internal/config/config.go`
- Test: `backend/internal/config/config_test.go`

- [ ] **Step 1: Add the failing test assertions**

In `config_test.go`, add this test at the end of the file:

```go
func TestConfig_AutomationDefaultsPresentInStruct(t *testing.T) {
	t.Parallel()
	c := config.Config{
		Ollama:     config.OllamaConfig{BaseURL: "http://ollama:11434", Model: "qwen2.5:0.5b", Timeout: time.Minute, MaxInputChars: 8000},
		Scheduler:  config.SchedulerConfig{Tick: 30 * time.Second, ClaimBatch: 5, Lease: 10 * time.Minute},
		Automation: config.AutomationConfig{MaxPostsPerRun: 5, DefaultInterval: time.Hour, HTTPTimeout: 15 * time.Second},
	}
	require.Equal(t, "qwen2.5:0.5b", c.Ollama.Model)
	require.Equal(t, 5, c.Scheduler.ClaimBatch)
	require.Equal(t, 5, c.Automation.MaxPostsPerRun)
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go -C backend test ./internal/config/ -run TestConfig_AutomationDefaults -v`
Expected: compile FAIL — `c.Ollama undefined` / `OllamaConfig` undefined.

- [ ] **Step 3: Add config keys**

In `keys.go`, append inside the `const ( … )` key block (after `keyJiraHTTPTimeout`):

```go

	keyOllamaBaseURL       = "ollama.base_url"
	keyOllamaModel         = "ollama.model"
	keyOllamaTimeout       = "ollama.timeout"
	keyOllamaMaxInputChars = "ollama.max_input_chars"

	keySchedulerTick       = "scheduler.tick"
	keySchedulerClaimBatch = "scheduler.claim_batch"
	keySchedulerLease      = "scheduler.lease"

	keyAutomationMaxPostsPerRun  = "automation.max_posts_per_run"
	keyAutomationDefaultInterval = "automation.default_interval"
	keyAutomationHTTPTimeout     = "automation.http_timeout"
```

- [ ] **Step 4: Add config structs**

In `config.go`, add `Ollama`, `Scheduler`, `Automation` fields to the `Config` struct (after `Jira JiraConfig …`):

```go
	Ollama     OllamaConfig     `mapstructure:"ollama"`
	Scheduler  SchedulerConfig  `mapstructure:"scheduler"`
	Automation AutomationConfig `mapstructure:"automation"`
```

Then add these struct definitions after `JiraConfig`:

```go
// OllamaConfig configures the local LLM used to summarize blog posts.
type OllamaConfig struct {
	BaseURL       string        `mapstructure:"base_url"`
	Model         string        `mapstructure:"model"`
	Timeout       time.Duration `mapstructure:"timeout"`
	MaxInputChars int           `mapstructure:"max_input_chars"`
}

// SchedulerConfig configures the automation scheduler worker.
type SchedulerConfig struct {
	Tick       time.Duration `mapstructure:"tick"`        // how often to poll for due automations
	ClaimBatch int           `mapstructure:"claim_batch"` // max automations claimed per tick
	Lease      time.Duration `mapstructure:"lease"`       // a running row older than this is reclaimable (crash self-heal)
}

// AutomationConfig configures a single automation run.
type AutomationConfig struct {
	MaxPostsPerRun  int           `mapstructure:"max_posts_per_run"` // cap per run; 0 = unlimited
	DefaultInterval time.Duration `mapstructure:"default_interval"`  // default scan interval for new automations
	HTTPTimeout     time.Duration `mapstructure:"http_timeout"`      // timeout for sitemap/scrape fetches
}
```

- [ ] **Step 5: Read the new keys in `Load`**

In `config.go` `Load`, add to the `cfg := Config{…}` literal (after the `Jira: JiraConfig{…},` block):

```go
		Ollama: OllamaConfig{
			BaseURL:       v.GetString(keyOllamaBaseURL),
			Model:         v.GetString(keyOllamaModel),
			Timeout:       v.GetDuration(keyOllamaTimeout),
			MaxInputChars: v.GetInt(keyOllamaMaxInputChars),
		},
		Scheduler: SchedulerConfig{
			Tick:       v.GetDuration(keySchedulerTick),
			ClaimBatch: v.GetInt(keySchedulerClaimBatch),
			Lease:      v.GetDuration(keySchedulerLease),
		},
		Automation: AutomationConfig{
			MaxPostsPerRun:  v.GetInt(keyAutomationMaxPostsPerRun),
			DefaultInterval: v.GetDuration(keyAutomationDefaultInterval),
			HTTPTimeout:     v.GetDuration(keyAutomationHTTPTimeout),
		},
```

- [ ] **Step 6: Add defaults**

In `config.go` `setDefaults`, append before the closing brace:

```go

	v.SetDefault(keyOllamaBaseURL, "http://ollama:11434")
	v.SetDefault(keyOllamaModel, "qwen2.5:0.5b")
	v.SetDefault(keyOllamaTimeout, "120s")
	v.SetDefault(keyOllamaMaxInputChars, 8000)

	v.SetDefault(keySchedulerTick, "30s")
	v.SetDefault(keySchedulerClaimBatch, 5)
	v.SetDefault(keySchedulerLease, "10m")

	v.SetDefault(keyAutomationMaxPostsPerRun, 5)
	v.SetDefault(keyAutomationDefaultInterval, "1h")
	v.SetDefault(keyAutomationHTTPTimeout, "15s")
```

- [ ] **Step 7: Run tests**

Run: `go -C backend test ./internal/config/ -v`
Expected: PASS (existing tests + new one).

- [ ] **Step 8: Commit**

```bash
git add backend/internal/config
git commit -m "feat(config): add ollama, scheduler, and automation config"
```

---

## Task 2: Domain — Automation entity

**Files:**
- Create: `backend/internal/domain/automation.go`
- Test: `backend/internal/domain/automation_test.go`

- [ ] **Step 1: Write the failing test**

`backend/internal/domain/automation_test.go`:

```go
package domain_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/domain"
)

func TestAutomationStatus_Values(t *testing.T) {
	t.Parallel()
	require.Equal(t, domain.AutomationStatus("idle"), domain.AutomationIdle)
	require.Equal(t, domain.AutomationStatus("running"), domain.AutomationRunning)
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go -C backend test ./internal/domain/ -run TestAutomationStatus -v`
Expected: compile FAIL — `domain.AutomationIdle` undefined.

- [ ] **Step 3: Write the domain type**

`backend/internal/domain/automation.go`:

```go
package domain

import (
	"time"

	"github.com/google/uuid"
)

// AutomationStatus is the run state of an automation row. It doubles as the claim
// lock: the scheduler flips idle->running to take a row, so a new scan never
// overlaps a running one.
type AutomationStatus string

const (
	AutomationIdle    AutomationStatus = "idle"
	AutomationRunning AutomationStatus = "running"
)

// Automation watches a site and files a Jira ticket per newly-discovered post.
// It is tenant-scoped and visible to the whole tenant; OwnerUserID is the user
// whose Jira credential the worker uses to file tickets.
type Automation struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	OwnerUserID uuid.UUID
	Name        string
	SiteURL     string
	Provider    string
	ProjectKey  string
	Interval    time.Duration
	Enabled     bool
	Status      AutomationStatus
	NextScanAt  time.Time
	LockedAt    *time.Time
	LastRunAt   *time.Time
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go -C backend test ./internal/domain/ -run TestAutomationStatus -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/domain/automation.go backend/internal/domain/automation_test.go
git commit -m "feat(domain): add Automation entity"
```

---

## Task 3: Migration — automations table, RLS, claim function

**Files:**
- Create: `backend/migrations/000002_automations.up.sql`
- Create: `backend/migrations/000002_automations.down.sql`

- [ ] **Step 1: Write the up migration**

`backend/migrations/000002_automations.up.sql`:

```sql
-- Automations: a tenant watches a site and files a Jira ticket per new post.
-- Tenant-scoped + RLS like the rest. The scheduler must find due rows across ALL
-- tenants before any tenant context exists, so the claim goes through a narrow
-- SECURITY DEFINER function (same pattern as find_user_for_login).

CREATE TYPE automation_status AS ENUM ('idle', 'running');

CREATE TABLE automations (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id        UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    owner_user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    site_url         TEXT NOT NULL,
    provider         TEXT NOT NULL DEFAULT 'jira',
    project_key      TEXT NOT NULL,
    interval_seconds INTEGER NOT NULL,
    enabled          BOOLEAN NOT NULL DEFAULT true,
    status           automation_status NOT NULL DEFAULT 'idle',
    next_scan_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    locked_at        TIMESTAMPTZ,
    last_run_at      TIMESTAMPTZ,
    last_error       TEXT,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Due-claim scans enabled rows ordered by next_scan_at; a partial index on the
-- ordering column serves it. tenant_id / owner_user_id index the FK cascades.
CREATE INDEX idx_automations_due ON automations (next_scan_at) WHERE enabled;
CREATE INDEX idx_automations_tenant_fk ON automations (tenant_id);
CREATE INDEX idx_automations_owner_fk ON automations (owner_user_id);

-- RLS: same tenant_isolation policy as the other tenant tables. The app role is
-- a non-owner, so ENABLE (not FORCE) is sufficient for it to apply.
ALTER TABLE automations ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON automations
    USING (tenant_id = app_current_tenant())
    WITH CHECK (tenant_id = app_current_tenant());

-- GRANT ALL TABLES from migration 1 only covered tables existing then; grant the
-- new table explicitly to the least-privilege app role.
GRANT SELECT, INSERT, UPDATE, DELETE ON automations TO identityhub_app;

-- Cross-tenant claim: atomically take up to p_limit due rows (idle, or running
-- past the lease = crashed), mark them running, and return them. SECURITY DEFINER
-- runs as the owner and bypasses RLS for this one narrow read/write.
CREATE OR REPLACE FUNCTION claim_due_automations(p_limit INT, p_lease INTERVAL)
RETURNS SETOF automations
LANGUAGE sql SECURITY DEFINER AS $$
    UPDATE automations
    SET status = 'running', locked_at = now()
    WHERE id IN (
        SELECT id FROM automations
        WHERE enabled
          AND next_scan_at <= now()
          AND (status = 'idle' OR (status = 'running' AND locked_at < now() - p_lease))
        ORDER BY next_scan_at
        FOR UPDATE SKIP LOCKED
        LIMIT p_limit
    )
    RETURNING *;
$$;
GRANT EXECUTE ON FUNCTION claim_due_automations(INT, INTERVAL) TO identityhub_app;
```

- [ ] **Step 2: Write the down migration**

`backend/migrations/000002_automations.down.sql`:

```sql
DROP FUNCTION IF EXISTS claim_due_automations(INT, INTERVAL);
DROP TABLE IF EXISTS automations;
DROP TYPE IF EXISTS automation_status;
```

- [ ] **Step 3: Verify the migration applies (and rolls back)**

Start infra and run migrations both ways:

Run: `task infra && task migrate && task migrate-down && task migrate`
Expected: no errors; `automations` table created, dropped, recreated.

(If you don't have a local DB, this is also exercised by the integration suite in Task 15.)

- [ ] **Step 4: Commit**

```bash
git add backend/migrations/000002_automations.up.sql backend/migrations/000002_automations.down.sql
git commit -m "feat(db): add automations table, RLS, and claim function"
```

---

## Task 4: Postgres automation repository

**Files:**
- Create: `backend/internal/storage/postgres/automation.go`
- Test: covered by Task 15 integration tests (needs a real DB).

- [ ] **Step 1: Write the repository**

`backend/internal/storage/postgres/automation.go`:

```go
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
```

- [ ] **Step 2: Add the `ErrAutomationNotFound` sentinel**

In `backend/internal/domain/errors.go`, add to the `Integration` group:

```go
	ErrAutomationNotFound     = errors.New("automation not found")
```

- [ ] **Step 3: Verify it compiles**

Run: `go -C backend build ./...`
Expected: builds (behavior is tested in Task 15).

- [ ] **Step 4: Commit**

```bash
git add backend/internal/storage/postgres/automation.go backend/internal/domain/errors.go
git commit -m "feat(storage): add postgres automation repository"
```

---

## Task 5: Redis seen-set

**Files:**
- Create: `backend/internal/storage/redis/automationseen.go`
- Test: covered by Task 15 integration tests (needs a real Redis).

- [ ] **Step 1: Write the seen-set adapter**

`backend/internal/storage/redis/automationseen.go`:

```go
package redis

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
)

// RedisAutomationSeenSet tracks which post URLs an automation has already turned
// into tickets, at `automation:seen:{id}`. A URL is added only after its ticket
// is created, so a failed post retries on the next run.
type RedisAutomationSeenSet struct {
	client *goredis.Client
}

// NewRedisAutomationSeenSet constructs the set adapter.
func NewRedisAutomationSeenSet(client *goredis.Client) *RedisAutomationSeenSet {
	return &RedisAutomationSeenSet{client: client}
}

func automationSeenKey(id uuid.UUID) string { return "automation:seen:" + id.String() }

// Unseen returns the subset of urls not yet recorded for the automation,
// preserving input order (newest-first from the discoverer).
func (s *RedisAutomationSeenSet) Unseen(ctx context.Context, automationID uuid.UUID, urls []string) ([]string, error) {
	if len(urls) == 0 {
		return nil, nil
	}
	members, err := s.client.SMembers(ctx, automationSeenKey(automationID)).Result()
	if err != nil {
		return nil, fmt.Errorf("read seen set: %w", err)
	}
	seen := make(map[string]struct{}, len(members))
	for _, m := range members {
		seen[m] = struct{}{}
	}
	out := make([]string, 0, len(urls))
	for _, u := range urls {
		if _, ok := seen[u]; !ok {
			out = append(out, u)
		}
	}
	return out, nil
}

// Add records a processed URL.
func (s *RedisAutomationSeenSet) Add(ctx context.Context, automationID uuid.UUID, url string) error {
	if err := s.client.SAdd(ctx, automationSeenKey(automationID), url).Err(); err != nil {
		return fmt.Errorf("add seen url: %w", err)
	}
	return nil
}

// Clear deletes the automation's seen set (called on delete).
func (s *RedisAutomationSeenSet) Clear(ctx context.Context, automationID uuid.UUID) error {
	if err := s.client.Del(ctx, automationSeenKey(automationID)).Err(); err != nil {
		return fmt.Errorf("clear seen set: %w", err)
	}
	return nil
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go -C backend build ./...`
Expected: builds.

- [ ] **Step 3: Commit**

```bash
git add backend/internal/storage/redis/automationseen.go
git commit -m "feat(storage): add redis automation seen-set"
```

---

## Task 6: Sitemap discovery adapter

**Files:**
- Create: `backend/internal/automation/discover/sitemap.go`
- Test: `backend/internal/automation/discover/sitemap_test.go`

- [ ] **Step 1: Write the failing test**

`backend/internal/automation/discover/sitemap_test.go`:

```go
package discover_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/automation/discover"
)

func TestSitemap_Discover_FiltersAndOrders(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>SITE/blog/old</loc><lastmod>2024-01-01</lastmod></url>
  <url><loc>SITE/blog/new</loc><lastmod>2025-06-01</lastmod></url>
  <url><loc>SITE/about</loc><lastmod>2025-06-02</lastmod></url>
</urlset>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Inject the server's base into the fixture (loc must be absolute).
	body := func() {} // placeholder to keep imports tidy
	_ = body

	d := discover.New(5 * time.Second)
	urls, err := d.Discover(context.Background(), srv.URL+"/blog")
	require.NoError(t, err)
	// Only /blog/* (not /about), newest lastmod first.
	require.Equal(t, []string{srv.URL + "/blog/new", srv.URL + "/blog/old"}, urls)
}

func TestSitemap_Discover_FollowsIndex(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>` + baseOf(r) + `/child.xml</loc></sitemap>
</sitemapindex>`))
	})
	mux.HandleFunc("/child.xml", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>` + baseOf(r) + `/blog/post-1</loc></url>
</urlset>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	d := discover.New(5 * time.Second)
	urls, err := d.Discover(context.Background(), srv.URL)
	require.NoError(t, err)
	require.Equal(t, []string{srv.URL + "/blog/post-1"}, urls)
}

func TestSitemap_Discover_NoSitemap(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	d := discover.New(5 * time.Second)
	_, err := d.Discover(context.Background(), srv.URL)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sitemap")
}

// baseOf returns the test server's scheme://host for building absolute locs.
func baseOf(r *http.Request) string { return "http://" + r.Host }
```

Note: the first test's fixture uses literal `SITE` — replace it at runtime. Adjust the first test's handler to substitute `SITE` with `"http://"+r.Host`:

```go
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>` + base + `/blog/old</loc><lastmod>2024-01-01</lastmod></url>
  <url><loc>` + base + `/blog/new</loc><lastmod>2025-06-01</lastmod></url>
  <url><loc>` + base + `/about</loc><lastmod>2025-06-02</lastmod></url>
</urlset>`))
	})
```

(Use this corrected handler; delete the `body`/`_ = body` placeholder lines.)

- [ ] **Step 2: Run it to verify it fails**

Run: `go -C backend test ./internal/automation/discover/ -v`
Expected: compile FAIL — package `discover` does not exist.

- [ ] **Step 3: Write the adapter**

`backend/internal/automation/discover/sitemap.go`:

```go
// Package discover finds candidate post URLs for an automation by reading the
// site's sitemap.xml (sitemap-index aware). It is intentionally the only
// discovery strategy: sites without a sitemap are reported as an error.
package discover

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Sitemap discovers URLs via sitemap.xml.
type Sitemap struct {
	http *http.Client
}

// New constructs the discoverer with a per-request timeout.
func New(timeout time.Duration) *Sitemap {
	return &Sitemap{http: &http.Client{Timeout: timeout}}
}

type sitemapDoc struct {
	URLs []struct {
		Loc     string `xml:"loc"`
		LastMod string `xml:"lastmod"`
	} `xml:"url"`
	Sitemaps []struct {
		Loc string `xml:"loc"`
	} `xml:"sitemap"`
}

// Discover returns post URLs under siteURL (prefix match), newest lastmod first.
func (s *Sitemap) Discover(ctx context.Context, siteURL string) ([]string, error) {
	base, err := url.Parse(siteURL)
	if err != nil {
		return nil, fmt.Errorf("parse site url: %w", err)
	}
	root := base.Scheme + "://" + base.Host
	prefix := strings.TrimRight(siteURL, "/")

	doc, err := s.fetch(ctx, root+"/sitemap.xml")
	if err != nil {
		return nil, fmt.Errorf("no sitemap.xml found at %s: %w", root, err)
	}

	type entry struct {
		loc     string
		lastmod string
	}
	var entries []entry

	if len(doc.Sitemaps) > 0 {
		// Sitemap index: follow one level of children.
		for _, sm := range doc.Sitemaps {
			child, err := s.fetch(ctx, sm.Loc)
			if err != nil {
				continue // skip an unreachable child sitemap
			}
			for _, u := range child.URLs {
				entries = append(entries, entry{loc: u.Loc, lastmod: u.LastMod})
			}
		}
	} else {
		for _, u := range doc.URLs {
			entries = append(entries, entry{loc: u.Loc, lastmod: u.LastMod})
		}
	}

	// Filter to URLs strictly under the watched prefix.
	filtered := entries[:0]
	for _, e := range entries {
		loc := strings.TrimSpace(e.loc)
		if strings.HasPrefix(loc, prefix) && len(loc) > len(prefix) {
			filtered = append(filtered, entry{loc: loc, lastmod: e.lastmod})
		}
	}
	if len(filtered) == 0 {
		return nil, fmt.Errorf("no posts under %s in sitemap", prefix)
	}

	// Newest lastmod first; entries without a lastmod sort last (stable).
	sort.SliceStable(filtered, func(i, j int) bool {
		return filtered[i].lastmod > filtered[j].lastmod
	})
	out := make([]string, len(filtered))
	for i, e := range filtered {
		out[i] = e.loc
	}
	return out, nil
}

func (s *Sitemap) fetch(ctx context.Context, u string) (*sitemapDoc, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // cap 10 MiB
	if err != nil {
		return nil, err
	}
	var doc sitemapDoc
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse sitemap xml: %w", err)
	}
	return &doc, nil
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go -C backend test ./internal/automation/discover/ -v`
Expected: PASS (all three tests).

- [ ] **Step 5: Commit**

```bash
git add backend/internal/automation/discover
git commit -m "feat(automation): add sitemap discovery adapter"
```

---

## Task 7: Scrape adapter (fetch → readability → markdown)

**Files:**
- Create: `backend/internal/automation/scrape/scrape.go`
- Test: `backend/internal/automation/scrape/scrape_test.go`
- Modify: `backend/go.mod` / `backend/go.sum` (new deps)

- [ ] **Step 1: Add the dependencies**

Run:
```bash
go -C backend get github.com/go-shiori/go-readability@latest
go -C backend get github.com/JohannesKaufmann/html-to-markdown/v2@latest
```
Expected: `go.mod` gains both requires.

- [ ] **Step 2: Write the failing test**

`backend/internal/automation/scrape/scrape_test.go`:

```go
package scrape_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/automation/scrape"
)

func TestScraper_Scrape_TitleAndMarkdown(t *testing.T) {
	t.Parallel()
	page := `<!DOCTYPE html><html><head><title>My Post Title</title></head>
<body>
  <nav>menu noise that should be stripped</nav>
  <article>
    <h1>My Post Title</h1>
    <p>This is the first paragraph of the article body with enough text to be
    considered the main content by the readability extractor, repeated to add
    length. This is the first paragraph of the article body with enough text.</p>
    <p>A second meaningful paragraph follows with more words so extraction works.</p>
  </article>
</body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(page))
	}))
	defer srv.Close()

	s := scrape.New(5 * time.Second)
	title, md, err := s.Scrape(context.Background(), srv.URL+"/blog/post")
	require.NoError(t, err)
	require.Equal(t, "My Post Title", title)
	require.Contains(t, md, "first paragraph")
	require.NotContains(t, strings.ToLower(md), "menu noise")
}

func TestScraper_Scrape_Non2xx(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	s := scrape.New(5 * time.Second)
	_, _, err := s.Scrape(context.Background(), srv.URL)
	require.Error(t, err)
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go -C backend test ./internal/automation/scrape/ -v`
Expected: compile FAIL — package `scrape` does not exist.

- [ ] **Step 4: Write the adapter**

`backend/internal/automation/scrape/scrape.go`:

```go
// Package scrape fetches a post URL and returns its title and a markdown render
// of the main content (boilerplate stripped via readability). No headless
// browser: JS-rendered pages are out of scope.
package scrape

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	readability "github.com/go-shiori/go-readability"
)

// Scraper fetches and converts post pages.
type Scraper struct {
	http *http.Client
}

// New constructs the scraper with a per-request timeout.
func New(timeout time.Duration) *Scraper {
	return &Scraper{http: &http.Client{Timeout: timeout}}
}

const userAgent = "IdentityHub-Automation/1.0 (+https://github.com/assafbh/identityhub)"

// Scrape returns the post title and markdown body for pageURL.
func (s *Scraper) Scrape(ctx context.Context, pageURL string) (title string, markdown string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := s.http.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("fetch post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("fetch post %s: status %d", pageURL, resp.StatusCode)
	}

	parsed, _ := url.Parse(pageURL)
	article, err := readability.FromReader(resp.Body, parsed)
	if err != nil {
		return "", "", fmt.Errorf("extract article: %w", err)
	}

	md, err := htmltomarkdown.ConvertString(article.Content)
	if err != nil {
		return "", "", fmt.Errorf("convert markdown: %w", err)
	}
	return article.Title, md, nil
}
```

- [ ] **Step 5: Run it to verify it passes**

Run: `go -C backend test ./internal/automation/scrape/ -v`
Expected: PASS.

> If the library APIs differ from the above (e.g. `readability.FromReader` signature or the `htmltomarkdown.ConvertString` import path), the test failure will point at it — adjust the call to the installed version's API. The behavior the test pins (title + main-content markdown, boilerplate stripped) is the contract.

- [ ] **Step 6: Tidy and commit**

```bash
go -C backend mod tidy
git add backend/internal/automation/scrape backend/go.mod backend/go.sum
git commit -m "feat(automation): add scrape adapter (readability -> markdown)"
```

---

## Task 8: Summarize adapter (Ollama client)

**Files:**
- Create: `backend/internal/automation/summarize/ollama.go`
- Test: `backend/internal/automation/summarize/ollama_test.go`

- [ ] **Step 1: Write the failing test**

`backend/internal/automation/summarize/ollama_test.go`:

```go
package summarize_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/automation/summarize"
)

func TestOllama_Summarize(t *testing.T) {
	t.Parallel()
	var gotModel, gotPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/generate", r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Model  string `json:"model"`
			Prompt string `json:"prompt"`
			Stream bool   `json:"stream"`
		}
		_ = json.Unmarshal(body, &req)
		gotModel, gotPrompt = req.Model, req.Prompt
		require.False(t, req.Stream)
		_ = json.NewEncoder(w).Encode(map[string]any{"response": "  A concise summary. "})
	}))
	defer srv.Close()

	o := summarize.New(srv.URL, "qwen2.5:0.5b", 10*time.Second, 20)
	out, err := o.Summarize(context.Background(), strings.Repeat("x", 100))
	require.NoError(t, err)
	require.Equal(t, "A concise summary.", out)
	require.Equal(t, "qwen2.5:0.5b", gotModel)
	require.LessOrEqual(t, len(gotPrompt), 20+200) // input truncated to ~maxInput + fixed instruction
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go -C backend test ./internal/automation/summarize/ -v`
Expected: compile FAIL — package `summarize` does not exist.

- [ ] **Step 3: Write the adapter**

`backend/internal/automation/summarize/ollama.go`:

```go
// Package summarize turns post markdown into a short summary via a local Ollama
// model. The HTTP call is non-streaming and the input is truncated to bound
// latency and memory for small models.
package summarize

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Ollama is a thin client for the Ollama /api/generate endpoint.
type Ollama struct {
	baseURL  string
	model    string
	http     *http.Client
	maxInput int
}

// New constructs the client. maxInput caps the markdown characters sent.
func New(baseURL, model string, timeout time.Duration, maxInput int) *Ollama {
	return &Ollama{
		baseURL:  strings.TrimRight(baseURL, "/"),
		model:    model,
		http:     &http.Client{Timeout: timeout},
		maxInput: maxInput,
	}
}

const promptTemplate = "Summarize the following blog post in 3-5 sentences for a Jira ticket. " +
	"Be factual and concise.\n\n---\n%s\n---\nSummary:"

type generateRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

type generateResponse struct {
	Response string `json:"response"`
}

// Summarize returns a short summary of the given markdown.
func (o *Ollama) Summarize(ctx context.Context, markdown string) (string, error) {
	in := markdown
	if o.maxInput > 0 && len(in) > o.maxInput {
		in = in[:o.maxInput]
	}
	reqBody, err := json.Marshal(generateRequest{
		Model:  o.model,
		Prompt: fmt.Sprintf(promptTemplate, in),
		Stream: false,
	})
	if err != nil {
		return "", fmt.Errorf("marshal ollama request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/generate", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("ollama returned %d", resp.StatusCode)
	}
	var out generateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode ollama response: %w", err)
	}
	summary := strings.TrimSpace(out.Response)
	if summary == "" {
		return "", fmt.Errorf("ollama returned an empty summary")
	}
	return summary, nil
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go -C backend test ./internal/automation/summarize/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add backend/internal/automation/summarize
git commit -m "feat(automation): add ollama summarize adapter"
```

---

## Task 9: Automation service (CRUD + RunOnce)

**Files:**
- Create: `backend/internal/automation/service.go`
- Test: `backend/internal/automation/service_test.go`

- [ ] **Step 1: Write the failing test**

`backend/internal/automation/service_test.go`:

```go
package automation_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/automation"
	"github.com/assafbh/identityhub/internal/domain"
)

// --- fakes ---

type fakeDisc struct{ urls []string }

func (f fakeDisc) Discover(context.Context, string) ([]string, error) { return f.urls, nil }

type fakeScraper struct{ err error }

func (f fakeScraper) Scrape(_ context.Context, u string) (string, string, error) {
	if f.err != nil {
		return "", "", f.err
	}
	return "Title of " + u, "# body " + u, nil
}

type fakeSumm struct{}

func (fakeSumm) Summarize(_ context.Context, md string) (string, error) { return "summary:" + md, nil }

type fakeTickets struct {
	created []domain.TicketPayload
	err     error
}

func (f *fakeTickets) CreateTicket(_ context.Context, _ domain.Identity, p domain.TicketPayload) (domain.TicketRef, error) {
	if f.err != nil {
		return domain.TicketRef{}, f.err
	}
	f.created = append(f.created, p)
	return domain.TicketRef{Provider: "jira", IssueKey: "NHI-1", URL: "http://j/NHI-1"}, nil
}

type fakeSeen struct{ added []string }

func (f *fakeSeen) Unseen(_ context.Context, _ uuid.UUID, urls []string) ([]string, error) {
	return urls, nil
}
func (f *fakeSeen) Add(_ context.Context, _ uuid.UUID, url string) error {
	f.added = append(f.added, url)
	return nil
}
func (f *fakeSeen) Clear(context.Context, uuid.UUID) error { return nil }

func testAutomation() domain.Automation {
	return domain.Automation{
		ID: uuid.New(), TenantID: uuid.New(), OwnerUserID: uuid.New(),
		SiteURL: "http://site/blog", ProjectKey: "NHI", Provider: domain.ProviderJira,
	}
}

func TestRunOnce_CreatesTicketsAndMarksSeen(t *testing.T) {
	t.Parallel()
	tickets := &fakeTickets{}
	seen := &fakeSeen{}
	svc := automation.NewService(automation.Deps{
		Disc: fakeDisc{urls: []string{"http://site/blog/a", "http://site/blog/b"}},
		Scraper: fakeScraper{}, Summ: fakeSumm{}, Tickets: tickets, Seen: seen,
		MaxPostsPerRun: 0,
	})
	err := svc.RunOnce(context.Background(), testAutomation())
	require.NoError(t, err)
	require.Len(t, tickets.created, 2)
	require.Equal(t, "NHI", tickets.created[0].ProjectKey)
	require.Contains(t, tickets.created[0].Description, "summary:")
	require.Equal(t, []string{"http://site/blog/a", "http://site/blog/b"}, seen.added)
}

func TestRunOnce_RespectsCap(t *testing.T) {
	t.Parallel()
	tickets := &fakeTickets{}
	seen := &fakeSeen{}
	svc := automation.NewService(automation.Deps{
		Disc: fakeDisc{urls: []string{"http://site/blog/a", "http://site/blog/b", "http://site/blog/c"}},
		Scraper: fakeScraper{}, Summ: fakeSumm{}, Tickets: tickets, Seen: seen,
		MaxPostsPerRun: 2,
	})
	require.NoError(t, svc.RunOnce(context.Background(), testAutomation()))
	require.Len(t, tickets.created, 2)
	require.Len(t, seen.added, 2)
}

func TestRunOnce_ReauthAbortsWithoutMarkingSeen(t *testing.T) {
	t.Parallel()
	tickets := &fakeTickets{err: domain.ErrReauthRequired}
	seen := &fakeSeen{}
	svc := automation.NewService(automation.Deps{
		Disc: fakeDisc{urls: []string{"http://site/blog/a"}},
		Scraper: fakeScraper{}, Summ: fakeSumm{}, Tickets: tickets, Seen: seen,
	})
	err := svc.RunOnce(context.Background(), testAutomation())
	require.ErrorIs(t, err, domain.ErrReauthRequired)
	require.Empty(t, seen.added)
}

func TestRunOnce_ScrapeFailureSkipsPostButContinues(t *testing.T) {
	t.Parallel()
	tickets := &fakeTickets{}
	seen := &fakeSeen{}
	svc := automation.NewService(automation.Deps{
		Disc: fakeDisc{urls: []string{"http://site/blog/a"}},
		Scraper: fakeScraper{err: errors.New("boom")}, Summ: fakeSumm{}, Tickets: tickets, Seen: seen,
	})
	require.NoError(t, svc.RunOnce(context.Background(), testAutomation()))
	require.Empty(t, tickets.created)
	require.Empty(t, seen.added) // not marked seen -> retried next run
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go -C backend test ./internal/automation/ -run TestRunOnce -v`
Expected: compile FAIL — package `automation` does not exist.

- [ ] **Step 3: Write the service**

`backend/internal/automation/service.go`:

```go
// Package automation is the blog-digest bounded context: it manages automations
// (CRUD) and runs a single automation's pipeline (discover -> diff unseen ->
// scrape -> summarize -> create ticket). It defines small consumer ports; the
// concrete adapters are wired in internal/app.
package automation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/logging"
)

// maxTitleLen mirrors the HTTP ticket title bound (Jira summary limit).
const maxTitleLen = 255

// Discoverer lists candidate post URLs for a site (newest first).
type Discoverer interface {
	Discover(ctx context.Context, siteURL string) ([]string, error)
}

// Scraper fetches a post and returns its title and markdown body.
type Scraper interface {
	Scrape(ctx context.Context, url string) (title string, markdown string, err error)
}

// Summarizer turns markdown into a short summary.
type Summarizer interface {
	Summarize(ctx context.Context, markdown string) (string, error)
}

// TicketCreator files a ticket. Satisfied by *ticketreport.Service.
type TicketCreator interface {
	CreateTicket(ctx context.Context, principal domain.Identity, payload domain.TicketPayload) (domain.TicketRef, error)
}

// SeenSet tracks processed URLs per automation.
type SeenSet interface {
	Unseen(ctx context.Context, automationID uuid.UUID, urls []string) ([]string, error)
	Add(ctx context.Context, automationID uuid.UUID, url string) error
	Clear(ctx context.Context, automationID uuid.UUID) error
}

// Repository persists automations.
type Repository interface {
	Create(ctx context.Context, a *domain.Automation) error
	Get(ctx context.Context, tenantID, id uuid.UUID) (domain.Automation, error)
	ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]domain.Automation, error)
	Update(ctx context.Context, a *domain.Automation) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
	SetDue(ctx context.Context, tenantID, id uuid.UUID, at time.Time) error
	ClaimDue(ctx context.Context, batch int, lease time.Duration) ([]domain.Automation, error)
	Complete(ctx context.Context, tenantID, id uuid.UUID, nextScanAt time.Time, runErr string) error
}

// Deps are the collaborators of a Service. Repo may be nil for a runner-only
// Service (the scheduler builds one without CRUD).
type Deps struct {
	Repo            Repository
	Disc            Discoverer
	Scraper         Scraper
	Summ            Summarizer
	Tickets         TicketCreator
	Seen            SeenSet
	MaxPostsPerRun  int
	DefaultInterval time.Duration
	Now             func() time.Time
}

// Service implements automation CRUD and the per-run pipeline.
type Service struct {
	repo            Repository
	disc            Discoverer
	scraper         Scraper
	summ            Summarizer
	tickets         TicketCreator
	seen            SeenSet
	maxPostsPerRun  int
	defaultInterval time.Duration
	now             func() time.Time
}

// NewService constructs the service.
func NewService(d Deps) *Service {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		repo: d.Repo, disc: d.Disc, scraper: d.Scraper, summ: d.Summ,
		tickets: d.Tickets, seen: d.Seen, maxPostsPerRun: d.MaxPostsPerRun,
		defaultInterval: d.DefaultInterval, now: now,
	}
}

// RunOnce executes one full pipeline for an automation. It returns
// ErrReauthRequired if the owner's Jira connection needs reconnecting (the run
// aborts, leaving remaining posts unseen for the next run). Per-post scrape /
// summarize / create failures are logged and skipped (left unseen to retry).
func (s *Service) RunOnce(ctx context.Context, a domain.Automation) error {
	log := logging.FromContext(ctx)

	urls, err := s.disc.Discover(ctx, a.SiteURL)
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}
	unseen, err := s.seen.Unseen(ctx, a.ID, urls)
	if err != nil {
		return fmt.Errorf("filter unseen: %w", err)
	}
	if s.maxPostsPerRun > 0 && len(unseen) > s.maxPostsPerRun {
		unseen = unseen[:s.maxPostsPerRun]
	}

	principal := domain.Identity{TenantID: a.TenantID, UserID: a.OwnerUserID}
	for _, postURL := range unseen {
		title, markdown, err := s.scraper.Scrape(ctx, postURL)
		if err != nil {
			log.Warn("scrape post failed", logging.Err(err))
			continue
		}
		summary, err := s.summ.Summarize(ctx, markdown)
		if err != nil {
			log.Warn("summarize post failed", logging.Err(err))
			continue
		}
		_, err = s.tickets.CreateTicket(ctx, principal, domain.TicketPayload{
			ProjectKey:  a.ProjectKey,
			Title:       truncate(title, maxTitleLen),
			Description: summary + "\n\nSource: " + postURL,
			Labels:      []string{"identityhub", "blog-digest"},
		})
		if err != nil {
			if errors.Is(err, domain.ErrReauthRequired) {
				return domain.ErrReauthRequired // no point continuing this run
			}
			log.Warn("create ticket failed", logging.Err(err))
			continue
		}
		if err := s.seen.Add(ctx, a.ID, postURL); err != nil {
			log.Warn("mark seen failed", logging.Err(err))
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
```

- [ ] **Step 4: Run it to verify it passes**

Run: `go -C backend test ./internal/automation/ -run TestRunOnce -v`
Expected: PASS (all four tests).

- [ ] **Step 5: Add the CRUD use-cases**

Append to `service.go`:

```go
// CreateInput is the data for a new automation (owner is the caller).
type CreateInput struct {
	Name       string
	SiteURL    string
	ProjectKey string
	Interval   time.Duration
	Enabled    bool
}

// UpdateInput holds optional field changes (nil = leave unchanged).
type UpdateInput struct {
	Name       *string
	SiteURL    *string
	ProjectKey *string
	Interval   *time.Duration
	Enabled    *bool
}

// Create makes a new automation owned by the caller, due immediately so the
// first scan runs on the next tick.
func (s *Service) Create(ctx context.Context, principal domain.Identity, in CreateInput) (domain.Automation, error) {
	interval := in.Interval
	if interval <= 0 {
		interval = s.defaultInterval
	}
	a := domain.Automation{
		TenantID:    principal.TenantID,
		OwnerUserID: principal.UserID,
		Name:        in.Name,
		SiteURL:     in.SiteURL,
		Provider:    domain.ProviderJira,
		ProjectKey:  in.ProjectKey,
		Interval:    interval,
		Enabled:     in.Enabled,
		NextScanAt:  s.now(),
	}
	if err := s.repo.Create(ctx, &a); err != nil {
		return domain.Automation{}, err
	}
	return a, nil
}

// List returns the tenant's automations.
func (s *Service) List(ctx context.Context, principal domain.Identity) ([]domain.Automation, error) {
	return s.repo.ListByTenant(ctx, principal.TenantID)
}

// Get returns one automation.
func (s *Service) Get(ctx context.Context, principal domain.Identity, id uuid.UUID) (domain.Automation, error) {
	return s.repo.Get(ctx, principal.TenantID, id)
}

// Update applies the supplied field changes.
func (s *Service) Update(ctx context.Context, principal domain.Identity, id uuid.UUID, in UpdateInput) (domain.Automation, error) {
	a, err := s.repo.Get(ctx, principal.TenantID, id)
	if err != nil {
		return domain.Automation{}, err
	}
	if in.Name != nil {
		a.Name = *in.Name
	}
	if in.SiteURL != nil {
		a.SiteURL = *in.SiteURL
	}
	if in.ProjectKey != nil {
		a.ProjectKey = *in.ProjectKey
	}
	if in.Interval != nil {
		a.Interval = *in.Interval
	}
	if in.Enabled != nil {
		a.Enabled = *in.Enabled
	}
	if err := s.repo.Update(ctx, &a); err != nil {
		return domain.Automation{}, err
	}
	return a, nil
}

// Delete removes an automation and clears its seen-set.
func (s *Service) Delete(ctx context.Context, principal domain.Identity, id uuid.UUID) error {
	if err := s.repo.Delete(ctx, principal.TenantID, id); err != nil {
		return err
	}
	return s.seen.Clear(ctx, id)
}

// RunNow makes an automation due immediately.
func (s *Service) RunNow(ctx context.Context, principal domain.Identity, id uuid.UUID) error {
	return s.repo.SetDue(ctx, principal.TenantID, id, s.now())
}
```

- [ ] **Step 6: Verify it still compiles + tests pass**

Run: `go -C backend test ./internal/automation/ -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add backend/internal/automation/service.go backend/internal/automation/service_test.go
git commit -m "feat(automation): add service (CRUD + RunOnce pipeline)"
```

---

## Task 10: Scheduler loop

**Files:**
- Create: `backend/internal/automation/scheduler.go`
- Test: `backend/internal/automation/scheduler_test.go`

- [ ] **Step 1: Write the failing test**

`backend/internal/automation/scheduler_test.go`:

```go
package automation_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/automation"
	"github.com/assafbh/identityhub/internal/domain"
)

// fakeRepo records ClaimDue/Complete. ClaimDue returns its queued batch once,
// then nothing.
type fakeRepo struct {
	mu        sync.Mutex
	batches   [][]domain.Automation
	completed []completion
}

type completion struct {
	id     uuid.UUID
	next   time.Time
	runErr string
}

func (r *fakeRepo) ClaimDue(_ context.Context, _ int, _ time.Duration) ([]domain.Automation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.batches) == 0 {
		return nil, nil
	}
	b := r.batches[0]
	r.batches = r.batches[1:]
	return b, nil
}

func (r *fakeRepo) Complete(_ context.Context, _, id uuid.UUID, next time.Time, runErr string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completed = append(r.completed, completion{id: id, next: next, runErr: runErr})
	return nil
}

// unused Repository methods (scheduler only needs ClaimDue/Complete).
func (r *fakeRepo) Create(context.Context, *domain.Automation) error                          { return nil }
func (r *fakeRepo) Get(context.Context, uuid.UUID, uuid.UUID) (domain.Automation, error)      { return domain.Automation{}, nil }
func (r *fakeRepo) ListByTenant(context.Context, uuid.UUID) ([]domain.Automation, error)      { return nil, nil }
func (r *fakeRepo) Update(context.Context, *domain.Automation) error                          { return nil }
func (r *fakeRepo) Delete(context.Context, uuid.UUID, uuid.UUID) error                        { return nil }
func (r *fakeRepo) SetDue(context.Context, uuid.UUID, uuid.UUID, time.Time) error             { return nil }

func TestScheduler_RunDue_RunsAndCompletes(t *testing.T) {
	t.Parallel()
	a := testAutomation()
	a.Interval = time.Hour
	repo := &fakeRepo{batches: [][]domain.Automation{{a}}}
	tickets := &fakeTickets{}
	svc := automation.NewService(automation.Deps{
		Disc: fakeDisc{urls: []string{"http://site/blog/a"}},
		Scraper: fakeScraper{}, Summ: fakeSumm{}, Tickets: tickets, Seen: &fakeSeen{},
	})
	fixedNow := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	sch := automation.NewScheduler(automation.SchedulerDeps{
		Service: svc, Repo: repo, Tick: time.Hour, Batch: 5, Lease: time.Minute,
		Now: func() time.Time { return fixedNow },
	})

	sch.RunDue(context.Background()) // one pass

	require.Len(t, tickets.created, 1)
	require.Len(t, repo.completed, 1)
	require.Equal(t, a.ID, repo.completed[0].id)
	require.Equal(t, fixedNow.Add(time.Hour), repo.completed[0].next)
	require.Empty(t, repo.completed[0].runErr)
}

func TestScheduler_RunDue_RecordsError(t *testing.T) {
	t.Parallel()
	a := testAutomation()
	a.Interval = time.Hour
	repo := &fakeRepo{batches: [][]domain.Automation{{a}}}
	svc := automation.NewService(automation.Deps{
		Disc: fakeDisc{urls: []string{"http://site/blog/a"}},
		Scraper: fakeScraper{}, Summ: fakeSumm{},
		Tickets: &fakeTickets{err: domain.ErrReauthRequired}, Seen: &fakeSeen{},
	})
	sch := automation.NewScheduler(automation.SchedulerDeps{
		Service: svc, Repo: repo, Tick: time.Hour, Batch: 5, Lease: time.Minute,
		Now: func() time.Time { return time.Unix(0, 0) },
	})
	sch.RunDue(context.Background())
	require.Len(t, repo.completed, 1)
	require.Contains(t, repo.completed[0].runErr, "reauth")
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go -C backend test ./internal/automation/ -run TestScheduler -v`
Expected: compile FAIL — `automation.NewScheduler` undefined.

- [ ] **Step 3: Write the scheduler**

`backend/internal/automation/scheduler.go`:

```go
package automation

import (
	"context"
	"log/slog"
	"time"

	"github.com/assafbh/identityhub/internal/logging"
)

// runner is the slice of Service the scheduler needs.
type runner interface {
	RunOnce(ctx context.Context, a domainAutomation) error
}

// claimRepo is the slice of Repository the scheduler needs.
type claimRepo interface {
	ClaimDue(ctx context.Context, batch int, lease time.Duration) ([]domainAutomation, error)
	Complete(ctx context.Context, tenantID, id uuid.UUIDAlias, nextScanAt time.Time, runErr string) error
}
```

> NOTE: do not use the aliases above — they are illustrative only. Write the real types directly, as below. (Replace the whole file with this:)

```go
package automation

import (
	"context"
	"log/slog"
	"time"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/logging"
)

// schedRunner is the slice of Service the scheduler needs.
type schedRunner interface {
	RunOnce(ctx context.Context, a domain.Automation) error
}

// Scheduler claims due automations and runs them one at a time (serial Ollama
// calls bound memory), then schedules the next scan.
type Scheduler struct {
	svc   schedRunner
	repo  Repository
	tick  time.Duration
	batch int
	lease time.Duration
	now   func() time.Time
	log   *slog.Logger
}

// SchedulerDeps are the collaborators of a Scheduler.
type SchedulerDeps struct {
	Service schedRunner
	Repo    Repository
	Tick    time.Duration
	Batch   int
	Lease   time.Duration
	Now     func() time.Time
	Logger  *slog.Logger
}

// NewScheduler constructs the scheduler.
func NewScheduler(d SchedulerDeps) *Scheduler {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{svc: d.Service, repo: d.Repo, tick: d.Tick, batch: d.Batch, lease: d.Lease, now: now, log: log}
}

// Run loops until ctx is cancelled, polling for due automations each tick.
func (s *Scheduler) Run(ctx context.Context) error {
	s.log.Info("automation scheduler started", slog.Duration("tick", s.tick))
	t := time.NewTicker(s.tick)
	defer t.Stop()
	for {
		s.RunDue(ctx)
		select {
		case <-ctx.Done():
			s.log.Info("automation scheduler stopping")
			return ctx.Err()
		case <-t.C:
		}
	}
}

// RunDue claims and processes one batch of due automations. Exposed for tests.
func (s *Scheduler) RunDue(ctx context.Context) {
	claimed, err := s.repo.ClaimDue(ctx, s.batch, s.lease)
	if err != nil {
		s.log.Error("claim due automations failed", logging.Err(err))
		return
	}
	for _, a := range claimed {
		runErr := s.svc.RunOnce(ctx, a)
		msg := ""
		if runErr != nil {
			msg = runErr.Error()
			s.log.Warn("automation run failed", slog.String("automation_id", a.ID.String()), logging.Err(runErr))
		}
		next := s.now().Add(a.Interval)
		if err := s.repo.Complete(ctx, a.TenantID, a.ID, next, msg); err != nil {
			s.log.Error("complete automation failed", slog.String("automation_id", a.ID.String()), logging.Err(err))
		}
	}
}
```

(Delete the first illustrative code block entirely; only the second `package automation … RunDue` block is the file content.)

- [ ] **Step 4: Run it to verify it passes**

Run: `go -C backend test ./internal/automation/ -run TestScheduler -v`
Expected: PASS.

- [ ] **Step 5: Run the whole automation package**

Run: `go -C backend test ./internal/automation/... -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add backend/internal/automation/scheduler.go backend/internal/automation/scheduler_test.go
git commit -m "feat(automation): add scheduler loop"
```

---

## Task 11: Wire automation + scheduler subcommand

**Files:**
- Modify: `backend/internal/app/app.go`
- Create: `backend/internal/app/scheduler.go`

- [ ] **Step 1: Add the scheduler subcommand dispatch**

In `app.go`, add a constant next to `CmdSeed`:

```go
// CmdScheduler runs the automation scheduler worker instead of the HTTP server.
const CmdScheduler = "scheduler"
```

In `Run`, replace the seed dispatch block with both subcommands:

```go
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case CmdSeed:
			return a.Seed(ctx)
		case CmdScheduler:
			return a.RunScheduler(ctx)
		}
	}
	return a.Serve(ctx)
```

- [ ] **Step 2: Build automation deps in `Wire`**

In `app.go`, add these imports:

```go
	"github.com/assafbh/identityhub/internal/automation"
	"github.com/assafbh/identityhub/internal/automation/discover"
	"github.com/assafbh/identityhub/internal/automation/scrape"
	"github.com/assafbh/identityhub/internal/automation/summarize"
```

Add `autoSvc` and `autoRepo` to the `App` struct:

```go
	autoSvc  *automation.Service
	autoRepo *store.PostgresAutomationRepository
```

In `Wire`, after `reportSvc := ticketreport.NewService(…)`, add:

```go
	// Automation (blog digest) subsystem.
	a.autoRepo = store.NewPostgresAutomationRepository(pool)
	automationSeen := redisstore.NewRedisAutomationSeenSet(redisClient)
	a.autoSvc = automation.NewService(automation.Deps{
		Repo:            a.autoRepo,
		Disc:            discover.New(cfg.Automation.HTTPTimeout),
		Scraper:         scrape.New(cfg.Automation.HTTPTimeout),
		Summ:            summarize.New(cfg.Ollama.BaseURL, cfg.Ollama.Model, cfg.Ollama.Timeout, cfg.Ollama.MaxInputChars),
		Tickets:         reportSvc,
		Seen:            automationSeen,
		MaxPostsPerRun:  cfg.Automation.MaxPostsPerRun,
		DefaultInterval: cfg.Automation.DefaultInterval,
	})
```

Then add the handler to `RouterDeps` (in the `a.deps = transport.RouterDeps{…}` literal):

```go
		Automation:  transport.NewAutomationHandler(a.autoSvc),
```

- [ ] **Step 3: Write `RunScheduler`**

`backend/internal/app/scheduler.go`:

```go
package app

import (
	"context"

	"github.com/assafbh/identityhub/internal/automation"
)

// RunScheduler runs the automation scheduler worker until ctx is cancelled. It
// reuses the same wiring as the API; only the entrypoint differs.
func (a *App) RunScheduler(ctx context.Context) error {
	sched := automation.NewScheduler(automation.SchedulerDeps{
		Service: a.autoSvc,
		Repo:    a.autoRepo,
		Tick:    a.cfg.Scheduler.Tick,
		Batch:   a.cfg.Scheduler.ClaimBatch,
		Lease:   a.cfg.Scheduler.Lease,
		Logger:  a.log,
	})
	a.log.Info("starting IdentityHub automation scheduler")
	err := sched.Run(ctx)
	if err == context.Canceled {
		return nil
	}
	return err
}
```

- [ ] **Step 4: Add the `Automation` field to RouterDeps**

(Done fully in Task 12 Step 4 — the field is added to `RouterDeps` there. For now this references a not-yet-existing field; complete Task 12 before building. To keep the build green between tasks, do Task 12 immediately after this task, or temporarily comment out the `Automation:` line until Task 12 is done.)

- [ ] **Step 5: Commit**

```bash
git add backend/internal/app
git commit -m "feat(app): wire automation subsystem + scheduler subcommand"
```

---

## Task 12: Transport — automation DTOs, handler, routes

**Files:**
- Modify: `backend/internal/transport/http/dto.go`
- Create: `backend/internal/transport/http/automation_handler.go`
- Modify: `backend/internal/transport/http/router.go`
- Test: `backend/internal/transport/http/automation_handler_test.go`

- [ ] **Step 1: Add DTOs**

Append to `dto.go`:

```go
// ---- automations ----

type automationRequest struct {
	Name            string `json:"name"`
	SiteURL         string `json:"site_url"`
	ProjectKey      string `json:"project_key"`
	IntervalSeconds int    `json:"interval_seconds"`
	Enabled         *bool  `json:"enabled"`
}

const minIntervalSeconds = 60

func (r automationRequest) validate() string {
	switch {
	case strings.TrimSpace(r.Name) == "":
		return "name is required"
	case !strings.HasPrefix(r.SiteURL, "http://") && !strings.HasPrefix(r.SiteURL, "https://"):
		return "site_url must be an http(s) URL"
	case strings.TrimSpace(r.ProjectKey) == "":
		return "project_key is required"
	case r.IntervalSeconds != 0 && r.IntervalSeconds < minIntervalSeconds:
		return "interval_seconds must be at least 60"
	default:
		return ""
	}
}

type automationResponse struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	SiteURL         string  `json:"site_url"`
	Provider        string  `json:"provider"`
	ProjectKey      string  `json:"project_key"`
	IntervalSeconds int     `json:"interval_seconds"`
	Enabled         bool    `json:"enabled"`
	Status          string  `json:"status"`
	NextScanAt      string  `json:"next_scan_at"`
	LastRunAt       *string `json:"last_run_at,omitempty"`
	LastError       string  `json:"last_error,omitempty"`
	CreatedAt       string  `json:"created_at"`
}

func toAutomationResponse(a domain.Automation) automationResponse {
	var lastRun *string
	if a.LastRunAt != nil {
		s := a.LastRunAt.UTC().Format(rfc3339)
		lastRun = &s
	}
	return automationResponse{
		ID:              a.ID.String(),
		Name:            a.Name,
		SiteURL:         a.SiteURL,
		Provider:        a.Provider,
		ProjectKey:      a.ProjectKey,
		IntervalSeconds: int(a.Interval.Seconds()),
		Enabled:         a.Enabled,
		Status:          string(a.Status),
		NextScanAt:      a.NextScanAt.UTC().Format(rfc3339),
		LastRunAt:       lastRun,
		LastError:       a.LastError,
		CreatedAt:       a.CreatedAt.UTC().Format(rfc3339),
	}
}
```

- [ ] **Step 2: Write the handler**

`backend/internal/transport/http/automation_handler.go`:

```go
package http

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/samber/lo"

	"github.com/assafbh/identityhub/internal/automation"
	"github.com/assafbh/identityhub/internal/domain"
)

// automationService is the slice of automation.Service the handler needs.
type automationService interface {
	Create(ctx context.Context, principal domain.Identity, in automation.CreateInput) (domain.Automation, error)
	List(ctx context.Context, principal domain.Identity) ([]domain.Automation, error)
	Get(ctx context.Context, principal domain.Identity, id uuid.UUID) (domain.Automation, error)
	Update(ctx context.Context, principal domain.Identity, id uuid.UUID, in automation.UpdateInput) (domain.Automation, error)
	Delete(ctx context.Context, principal domain.Identity, id uuid.UUID) error
	RunNow(ctx context.Context, principal domain.Identity, id uuid.UUID) error
}

// AutomationHandler serves the automation CRUD + run-now endpoints.
type AutomationHandler struct {
	svc automationService
}

// NewAutomationHandler constructs the handler.
func NewAutomationHandler(svc automationService) *AutomationHandler {
	return &AutomationHandler{svc: svc}
}

// List returns the tenant's automations.
func (h *AutomationHandler) List(c *gin.Context) {
	id, _ := mustIdentity(c)
	items, err := h.svc.List(c.Request.Context(), id)
	if err != nil {
		respondError(c, err)
		return
	}
	out := lo.Map(items, func(a domain.Automation, _ int) automationResponse { return toAutomationResponse(a) })
	c.JSON(http.StatusOK, gin.H{"automations": out})
}

// Create makes a new automation.
func (h *AutomationHandler) Create(c *gin.Context) {
	id, _ := mustIdentity(c)
	var req automationRequest
	if err := bindJSON(c, &req); err != nil {
		respondValidation(c, "invalid request body")
		return
	}
	if msg := req.validate(); msg != "" {
		respondValidation(c, msg)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	a, err := h.svc.Create(c.Request.Context(), id, automation.CreateInput{
		Name:       req.Name,
		SiteURL:    req.SiteURL,
		ProjectKey: req.ProjectKey,
		Interval:   time.Duration(req.IntervalSeconds) * time.Second,
		Enabled:    enabled,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toAutomationResponse(a))
}

// Get returns one automation.
func (h *AutomationHandler) Get(c *gin.Context) {
	id, _ := mustIdentity(c)
	aid, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondValidation(c, "invalid automation id")
		return
	}
	a, err := h.svc.Get(c.Request.Context(), id, aid)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAutomationResponse(a))
}

// Update applies field changes.
func (h *AutomationHandler) Update(c *gin.Context) {
	id, _ := mustIdentity(c)
	aid, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondValidation(c, "invalid automation id")
		return
	}
	var req automationRequest
	if err := bindJSON(c, &req); err != nil {
		respondValidation(c, "invalid request body")
		return
	}
	if msg := req.validate(); msg != "" {
		respondValidation(c, msg)
		return
	}
	in := automation.UpdateInput{Name: &req.Name, SiteURL: &req.SiteURL, ProjectKey: &req.ProjectKey, Enabled: req.Enabled}
	if req.IntervalSeconds != 0 {
		d := time.Duration(req.IntervalSeconds) * time.Second
		in.Interval = &d
	}
	a, err := h.svc.Update(c.Request.Context(), id, aid, in)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAutomationResponse(a))
}

// Delete removes an automation.
func (h *AutomationHandler) Delete(c *gin.Context) {
	id, _ := mustIdentity(c)
	aid, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondValidation(c, "invalid automation id")
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id, aid); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// RunNow makes an automation due immediately.
func (h *AutomationHandler) RunNow(c *gin.Context) {
	id, _ := mustIdentity(c)
	aid, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondValidation(c, "invalid automation id")
		return
	}
	if err := h.svc.RunNow(c.Request.Context(), id, aid); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusAccepted)
}
```

- [ ] **Step 3: Map `ErrAutomationNotFound` to 404**

In `errors.go`, add a case to `respondError` (before the `default:`):

```go
	case errors.Is(err, domain.ErrAutomationNotFound):
		c.JSON(http.StatusNotFound, errorResponse{Error: errCodeNotFound, Message: "automation not found"})
```

- [ ] **Step 4: Add routes and the RouterDeps field**

In `router.go`, add to `RouterDeps`:

```go
	Automation  *AutomationHandler
```

In `NewRouter`, inside the `v1` group (after the integration block), add:

```go
			if d.Automation != nil {
				registerAutomationRoutes(v1, d.Automation)
			}
```

Add the route registrar at the end of `router.go`:

```go
// registerAutomationRoutes mounts the automation CRUD endpoints. These are
// session-driven (UI); unsafe methods are guarded by CSRF + session method.
func registerAutomationRoutes(v1 *gin.RouterGroup, h *AutomationHandler) {
	ag := v1.Group("/automations")
	{
		ag.GET("", h.List)
		ag.POST("", RequireSessionMethod(), h.Create)
		ag.GET("/:id", h.Get)
		ag.PATCH("/:id", RequireSessionMethod(), h.Update)
		ag.DELETE("/:id", RequireSessionMethod(), h.Delete)
		ag.POST("/:id/run", RequireSessionMethod(), h.RunNow)
	}
}
```

- [ ] **Step 5: Write the handler test**

`backend/internal/transport/http/automation_handler_test.go`:

```go
package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/automation"
	"github.com/assafbh/identityhub/internal/domain"
)

type stubAutomationSvc struct {
	created automation.CreateInput
	listOut []domain.Automation
}

func (s *stubAutomationSvc) Create(_ context.Context, _ domain.Identity, in automation.CreateInput) (domain.Automation, error) {
	s.created = in
	return domain.Automation{ID: uuid.New(), Name: in.Name, SiteURL: in.SiteURL, ProjectKey: in.ProjectKey, Provider: domain.ProviderJira, Interval: in.Interval, Enabled: in.Enabled, Status: domain.AutomationIdle}, nil
}
func (s *stubAutomationSvc) List(context.Context, domain.Identity) ([]domain.Automation, error) {
	return s.listOut, nil
}
func (s *stubAutomationSvc) Get(context.Context, domain.Identity, uuid.UUID) (domain.Automation, error) {
	return domain.Automation{}, domain.ErrAutomationNotFound
}
func (s *stubAutomationSvc) Update(context.Context, domain.Identity, uuid.UUID, automation.UpdateInput) (domain.Automation, error) {
	return domain.Automation{}, nil
}
func (s *stubAutomationSvc) Delete(context.Context, domain.Identity, uuid.UUID) error { return nil }
func (s *stubAutomationSvc) RunNow(context.Context, domain.Identity, uuid.UUID) error { return nil }

func newTestContext(method, body string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest(method, "/", bytes.NewBufferString(body))
	setIdentity(c, domain.Identity{UserID: uuid.New(), TenantID: uuid.New(), AuthMethod: domain.AuthMethodSession})
	c.Request = req.WithContext(c.Request.Context())
	// re-attach identity after replacing the request
	setIdentity(c, domain.Identity{UserID: uuid.New(), TenantID: uuid.New(), AuthMethod: domain.AuthMethodSession})
	return c, w
}

func TestAutomationHandler_Create_Valid(t *testing.T) {
	t.Parallel()
	svc := &stubAutomationSvc{}
	h := NewAutomationHandler(svc)
	c, w := newTestContext(http.MethodPost, `{"name":"Blog","site_url":"https://x.com/blog","project_key":"NHI","interval_seconds":3600}`)
	h.Create(c)
	require.Equal(t, http.StatusCreated, w.Code)
	require.Equal(t, "Blog", svc.created.Name)
	require.Equal(t, "NHI", svc.created.ProjectKey)
}

func TestAutomationHandler_Create_InvalidURL(t *testing.T) {
	t.Parallel()
	h := NewAutomationHandler(&stubAutomationSvc{})
	c, w := newTestContext(http.MethodPost, `{"name":"Blog","site_url":"ftp://x","project_key":"NHI"}`)
	h.Create(c)
	require.Equal(t, http.StatusBadRequest, w.Code)
	var env map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &env)
	require.Equal(t, "invalid_request", env["error"])
}

func TestAutomationHandler_Get_NotFound(t *testing.T) {
	t.Parallel()
	h := NewAutomationHandler(&stubAutomationSvc{})
	c, w := newTestContext(http.MethodGet, "")
	c.Params = gin.Params{{Key: "id", Value: uuid.New().String()}}
	h.Get(c)
	require.Equal(t, http.StatusNotFound, w.Code)
}
```

> If `newTestContext` interferes with the request context wiring in this codebase's test conventions, mirror the pattern used in the existing `handler_test.go` / `integration_handler_test.go` for building a `*gin.Context` with an identity. The assertions (201 on valid create, 400 on bad URL, 404 on not-found) are the contract.

- [ ] **Step 6: Run the transport tests**

Run: `go -C backend test ./internal/transport/http/ -run TestAutomation -v`
Expected: PASS.

- [ ] **Step 7: Build everything (incl. the app wiring from Task 11)**

Run: `go -C backend build ./... && go -C backend vet ./...`
Expected: builds clean.

- [ ] **Step 8: Commit**

```bash
git add backend/internal/transport/http
git commit -m "feat(transport): add automation CRUD endpoints"
```

---

## Task 13: Deployment — Ollama image, compose, env, docs

**Files:**
- Create: `deployments/docker/ollama.Dockerfile`
- Modify: `docker-compose.yml`, `.env.example`, `README.md`, `Taskfile.yml`

- [ ] **Step 1: Write the pre-warmed Ollama Dockerfile**

`deployments/docker/ollama.Dockerfile`:

```dockerfile
# Pre-warmed Ollama image: bakes the summarization model into the image at build
# time so the container starts ready (no first-request pull). Override the model
# with --build-arg MODEL=... (and rebuild) to change it.
FROM ollama/ollama:latest

ARG MODEL=qwen2.5:0.5b
ENV PREPULL_MODEL=${MODEL}

# Start the server, pull the model, then stop. The layer keeps the model blob.
RUN ollama serve & \
    server_pid=$! && \
    for i in $(seq 1 30); do \
      ollama list >/dev/null 2>&1 && break; \
      sleep 1; \
    done && \
    ollama pull "${PREPULL_MODEL}" && \
    kill "${server_pid}"

# The base image already exposes 11434 and sets the entrypoint to `ollama serve`.
```

- [ ] **Step 2: Add the `ollama` and `scheduler` compose services**

In `docker-compose.yml`, add to `services:` (after `frontend:` or anywhere within `services`):

```yaml
  ollama:
    build:
      context: .
      dockerfile: deployments/docker/ollama.Dockerfile
      args:
        MODEL: "${OLLAMA_MODEL:-qwen2.5:0.5b}"
    # Memory cap: qwen2.5:0.5b is tiny; bound it so it can't balloon the host.
    mem_limit: "${OLLAMA_MEM_LIMIT:-1500m}"
    deploy:
      resources:
        limits:
          memory: "${OLLAMA_MEM_LIMIT:-1500m}"
    volumes:
      - ollamadata:/root/.ollama
    networks: [ihnet]

  # Automation worker: same image as the backend, runs the `scheduler` subcommand.
  scheduler:
    build:
      context: .
      dockerfile: deployments/docker/Dockerfile
    command: ["scheduler"]
    env_file: [.env]
    environment:
      <<: *app-env
      OLLAMA_BASE_URL: "${OLLAMA_BASE_URL:-http://ollama:11434}"
    mem_limit: "${SCHEDULER_MEM_LIMIT:-256m}"
    depends_on:
      postgres:
        condition: service_healthy
      redis:
        condition: service_healthy
      migrate:
        condition: service_completed_successfully
      ollama:
        condition: service_started
    networks: [ihnet]
```

Then add the volume under the top-level `volumes:` block:

```yaml
  ollamadata:
```

- [ ] **Step 3: Add env to `.env.example`**

Append to `.env.example`:

```bash

# --- Automation (blog digest) + Ollama -----------------------------------
# Local summarization model (pre-pulled into the ollama image at build time).
OLLAMA_BASE_URL=http://ollama:11434
OLLAMA_MODEL=qwen2.5:0.5b
OLLAMA_TIMEOUT=120s
OLLAMA_MAX_INPUT_CHARS=8000
# Memory caps for the heavy/worker containers.
OLLAMA_MEM_LIMIT=1500m
SCHEDULER_MEM_LIMIT=256m
# Scheduler polling + claim behavior.
SCHEDULER_TICK=30s
SCHEDULER_CLAIM_BATCH=5
SCHEDULER_LEASE=10m
# Per-run cap (drains a backlog over successive runs) and default scan interval.
AUTOMATION_MAX_POSTS_PER_RUN=5
AUTOMATION_DEFAULT_INTERVAL=1h
AUTOMATION_HTTP_TIMEOUT=15s
```

- [ ] **Step 4: Document the feature in `README.md`**

Replace the line in the "Assumptions & scope" section that says the blog digest is out of scope:

```markdown
- The `NHI Blog Digest` bonus is intentionally out of scope.
```

with:

```markdown
- **NHI Automation (blog digest).** The bonus is implemented as a generalized
  *Automation* tab: watch a site URL; a separate `scheduler` container periodically
  discovers new posts via `sitemap.xml`, scrapes each to markdown, summarizes it
  with a local, memory-limited Ollama model (`qwen2.5:0.5b`, configurable), and
  files a Jira ticket per post into a chosen project. Automations are tenant-shared
  and use the creator's Jira credential; processed URLs are tracked in Redis so each
  post is filed once. See `docs/superpowers/specs/2026-06-06-nhi-automation-blog-digest-design.md`.
```

- [ ] **Step 5: Note the scheduler in `Taskfile.yml` (optional helper)**

Add a task under `tasks:` in `Taskfile.yml`:

```yaml
  scheduler:
    desc: Run the automation scheduler worker locally (expects infra up)
    cmds:
      - go -C {{.BACKEND}} run ./cmd/api scheduler
```

- [ ] **Step 6: Verify compose config parses**

Run: `docker compose config >/dev/null && echo OK`
Expected: `OK` (no YAML/interpolation errors). Requires a `.env` (copy from `.env.example`).

- [ ] **Step 7: Commit**

```bash
git add deployments/docker/ollama.Dockerfile docker-compose.yml .env.example README.md Taskfile.yml
git commit -m "feat(deploy): add ollama image + scheduler service + automation env"
```

---

## Task 14: Frontend — Automation tab

**Files:**
- Modify: `frontend/src/types.ts`
- Modify: `frontend/src/api.ts`
- Create: `frontend/src/components/AutomationPanel.tsx`
- Modify: `frontend/src/components/Dashboard.tsx`

- [ ] **Step 1: Add the type**

Append to `frontend/src/types.ts`:

```ts
export interface Automation {
  id: string;
  name: string;
  site_url: string;
  provider: string;
  project_key: string;
  interval_seconds: number;
  enabled: boolean;
  status: string;
  next_scan_at: string;
  last_run_at?: string;
  last_error?: string;
  created_at: string;
}
```

- [ ] **Step 2: Add a `patch` method to the api client**

In `frontend/src/api.ts`, add `patch` to the exported `api` object:

```ts
export const api = {
  get: <T>(path: string) => request<T>(path),
  post: <T>(path: string, body?: unknown) => request<T>(path, { method: "POST", body }),
  patch: <T>(path: string, body?: unknown) => request<T>(path, { method: "PATCH", body }),
  del: <T>(path: string) => request<T>(path, { method: "DELETE" }),
};
```

- [ ] **Step 3: Write the Automation panel**

`frontend/src/components/AutomationPanel.tsx`:

```tsx
import { useCallback, useEffect, useState } from "react";
import { api } from "../api";
import { ApiError, type Automation, type Project } from "../types";

export function AutomationPanel({ projects, hasJira }: { projects: Project[]; hasJira: boolean }) {
  const [items, setItems] = useState<Automation[]>([]);
  const [name, setName] = useState("");
  const [siteUrl, setSiteUrl] = useState("");
  const [projectKey, setProjectKey] = useState(projects[0]?.key ?? "");
  const [manual, setManual] = useState(projects.length === 0);
  const [intervalMin, setIntervalMin] = useState(60);
  const [msg, setMsg] = useState<{ kind: "ok" | "error"; text: string } | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const res = await api.get<{ automations: Automation[] }>("/v1/automations");
      setItems(res.automations ?? []);
    } catch {
      setItems([]);
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  async function create(e: React.FormEvent) {
    e.preventDefault();
    setMsg(null);
    if (!name.trim() || !siteUrl.trim() || !projectKey.trim()) {
      setMsg({ kind: "error", text: "Name, site URL, and project are required." });
      return;
    }
    setBusy(true);
    try {
      await api.post<Automation>("/v1/automations", {
        name,
        site_url: siteUrl,
        project_key: projectKey,
        interval_seconds: intervalMin * 60,
      });
      setName("");
      setSiteUrl("");
      setMsg({ kind: "ok", text: "Automation created." });
      await load();
    } catch (err) {
      setMsg({ kind: "error", text: err instanceof ApiError ? err.message : "Failed to create automation." });
    } finally {
      setBusy(false);
    }
  }

  async function toggle(a: Automation) {
    await api.patch(`/v1/automations/${a.id}`, {
      name: a.name,
      site_url: a.site_url,
      project_key: a.project_key,
      interval_seconds: a.interval_seconds,
      enabled: !a.enabled,
    });
    await load();
  }

  async function runNow(a: Automation) {
    await api.post(`/v1/automations/${a.id}/run`);
    await load();
  }

  async function remove(a: Automation) {
    await api.del(`/v1/automations/${a.id}`);
    await load();
  }

  return (
    <>
      <section className="card span2">
        <h2>New automation</h2>
        {!hasJira && (
          <div className="alert warn">
            Connect Jira first — automations file tickets using your Jira connection.
          </div>
        )}
        <form onSubmit={create}>
          <label>
            Name
            <input placeholder="Atlassian blog watcher" value={name} onChange={(e) => setName(e.target.value)} />
          </label>
          <label>
            Site URL
            <input placeholder="https://www.atlassian.com/blog" value={siteUrl} onChange={(e) => setSiteUrl(e.target.value)} />
          </label>
          <label>
            Project
            {manual || projects.length === 0 ? (
              <input placeholder="e.g. NHI" value={projectKey} onChange={(e) => setProjectKey(e.target.value.toUpperCase())} />
            ) : (
              <select value={projectKey} onChange={(e) => setProjectKey(e.target.value)}>
                {projects.map((p) => (
                  <option key={p.key} value={p.key}>
                    {p.name} ({p.key})
                  </option>
                ))}
              </select>
            )}
          </label>
          {projects.length > 0 && (
            <button type="button" className="link small" onClick={() => setManual((m) => !m)}>
              {manual ? "Pick from list" : "Enter key manually"}
            </button>
          )}
          <label>
            Scan every (minutes)
            <input type="number" min={1} value={intervalMin} onChange={(e) => setIntervalMin(Number(e.target.value))} />
          </label>
          {msg && <div className={`alert ${msg.kind === "ok" ? "info" : "error"}`}>{msg.text}</div>}
          <button className="primary" type="submit" disabled={busy}>
            {busy ? "Creating…" : "Create automation"}
          </button>
        </form>
      </section>

      <section className="card span2">
        <h2>Automations</h2>
        {items.length === 0 ? (
          <p className="muted">No automations yet.</p>
        ) : (
          <ul className="ticket-list">
            {items.map((a) => (
              <li key={a.id}>
                <div>
                  <span className="key">{a.project_key}</span>
                  <span className="title">{a.name}</span>
                  <span className="muted"> · {a.site_url}</span>
                  <div className="muted small">
                    {a.enabled ? "Enabled" : "Disabled"} · every {Math.round(a.interval_seconds / 60)}m · {a.status}
                    {a.last_error ? ` · ⚠ ${a.last_error}` : ""}
                  </div>
                </div>
                <div className="row gap">
                  <button className="link small" onClick={() => runNow(a)}>Run now</button>
                  <button className="link small" onClick={() => toggle(a)}>{a.enabled ? "Disable" : "Enable"}</button>
                  <button className="link small" onClick={() => remove(a)}>Delete</button>
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>
    </>
  );
}
```

- [ ] **Step 4: Add the tab to the Dashboard**

In `frontend/src/components/Dashboard.tsx`:

Add the import:

```tsx
import { AutomationPanel } from "./AutomationPanel";
```

Add a tab state after the existing `useState` hooks:

```tsx
  const [tab, setTab] = useState<"findings" | "automation">("findings");
```

Replace the JSX from the Jira-integration `<section>` down through `<TokensPanel />` with a tab bar + conditional panels. Concretely, change the returned tree so that after the `{notice && …}` line it renders:

```tsx
      <nav className="tabs span2">
        <button className={tab === "findings" ? "tab active" : "tab"} onClick={() => setTab("findings")}>
          Findings
        </button>
        <button className={tab === "automation" ? "tab active" : "tab"} onClick={() => setTab("automation")}>
          Automation
        </button>
      </nav>

      <section className="card span2">
        <div className="row spread">
          <div>
            <h2>Jira integration</h2>
            <ConnectionBadge conn={conn} />
          </div>
          <div className="row gap">
            {conn?.connected ? (
              <button className="ghost" onClick={disconnect}>Disconnect</button>
            ) : (
              <button className="primary" onClick={connect}>Connect Jira</button>
            )}
            {conn && !conn.connected && conn.status === "needs_reauth" && (
              <button className="primary" onClick={connect}>Reconnect</button>
            )}
          </div>
        </div>
      </section>

      {tab === "findings" ? (
        conn?.connected ? (
          <TicketsPanel projects={projects} onReconnect={connect} />
        ) : (
          <section className="card span2 muted">
            Connect your Jira workspace to start reporting NHI findings.
          </section>
        )
      ) : (
        <AutomationPanel projects={projects} hasJira={!!conn?.connected} />
      )}

      {tab === "findings" && <TokensPanel />}
```

- [ ] **Step 5: Add minimal tab styling**

Append to `frontend/src/styles.css`:

```css
.tabs { display: flex; gap: 8px; margin-bottom: 4px; }
.tab { background: none; border: none; padding: 8px 14px; cursor: pointer; color: var(--muted, #888); border-bottom: 2px solid transparent; }
.tab.active { color: inherit; border-bottom-color: currentColor; font-weight: 600; }
.small { font-size: 0.85em; }
```

- [ ] **Step 6: Verify the frontend builds**

Run: `cd frontend && npm install && npm run build`
Expected: `tsc` + `vite build` succeed with no type errors.

- [ ] **Step 7: Commit**

```bash
git add frontend/src
git commit -m "feat(frontend): add Automation tab"
```

---

## Task 15: Integration tests (repo + e2e)

**Files:**
- Create: `backend/test/integration/automation_test.go`

- [ ] **Step 1: Write the repository + claim integration test**

`backend/test/integration/automation_test.go`:

```go
//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/domain"
	store "github.com/assafbh/identityhub/internal/storage/postgres"
)

// seedTenantUser inserts a tenant + user directly (admin pool, bypasses RLS) and
// returns their IDs for use as an automation owner.
func seedTenantUser(t *testing.T, ctx context.Context) (uuid.UUID, uuid.UUID) {
	t.Helper()
	var tenantID, userID uuid.UUID
	err := adminPool.QueryRow(ctx,
		`INSERT INTO tenants (slug, name) VALUES ($1, 'T') RETURNING id`, "t-"+uuid.NewString()[:8]).Scan(&tenantID)
	require.NoError(t, err)
	err = adminPool.QueryRow(ctx,
		`INSERT INTO users (tenant_id, email, password_hash) VALUES ($1, $2, 'x') RETURNING id`,
		tenantID, uuid.NewString()+"@t.test").Scan(&userID)
	require.NoError(t, err)
	return tenantID, userID
}

func TestAutomationRepository_CRUDAndClaim(t *testing.T) {
	ctx := context.Background()
	repo := store.NewPostgresAutomationRepository(appPool)
	tenantID, userID := seedTenantUser(t, ctx)

	a := &domain.Automation{
		TenantID: tenantID, OwnerUserID: userID, Name: "Blog", SiteURL: "https://x.com/blog",
		Provider: domain.ProviderJira, ProjectKey: "NHI", Interval: time.Hour, Enabled: true,
		NextScanAt: time.Now().Add(-time.Minute), // due
	}
	require.NoError(t, repo.Create(ctx, a))
	require.NotEqual(t, uuid.Nil, a.ID)

	got, err := repo.Get(ctx, tenantID, a.ID)
	require.NoError(t, err)
	require.Equal(t, "Blog", got.Name)
	require.Equal(t, time.Hour, got.Interval)

	list, err := repo.ListByTenant(ctx, tenantID)
	require.NoError(t, err)
	require.Len(t, list, 1)

	// Claim: the due row is returned and flipped to running.
	claimed, err := repo.ClaimDue(ctx, 10, 10*time.Minute)
	require.NoError(t, err)
	require.True(t, containsAutomation(claimed, a.ID))

	// A second claim does NOT return it again (now running, not past lease).
	again, err := repo.ClaimDue(ctx, 10, 10*time.Minute)
	require.NoError(t, err)
	require.False(t, containsAutomation(again, a.ID))

	// Complete reschedules and clears the lock.
	next := time.Now().Add(time.Hour)
	require.NoError(t, repo.Complete(ctx, tenantID, a.ID, next, ""))
	after, err := repo.Get(ctx, tenantID, a.ID)
	require.NoError(t, err)
	require.Equal(t, domain.AutomationIdle, after.Status)
	require.WithinDuration(t, next, after.NextScanAt, time.Second)

	require.NoError(t, repo.Delete(ctx, tenantID, a.ID))
	_, err = repo.Get(ctx, tenantID, a.ID)
	require.ErrorIs(t, err, domain.ErrAutomationNotFound)
}

func TestAutomationRepository_RLSIsolation(t *testing.T) {
	ctx := context.Background()
	repo := store.NewPostgresAutomationRepository(appPool)
	tA, uA := seedTenantUser(t, ctx)
	tB, _ := seedTenantUser(t, ctx)

	a := &domain.Automation{
		TenantID: tA, OwnerUserID: uA, Name: "A", SiteURL: "https://a/blog",
		Provider: domain.ProviderJira, ProjectKey: "NHI", Interval: time.Hour, Enabled: true,
		NextScanAt: time.Now(),
	}
	require.NoError(t, repo.Create(ctx, a))

	// Tenant B cannot see tenant A's automation.
	_, err := repo.Get(ctx, tB, a.ID)
	require.ErrorIs(t, err, domain.ErrAutomationNotFound)
	listB, err := repo.ListByTenant(ctx, tB)
	require.NoError(t, err)
	require.Empty(t, listB)
}

func containsAutomation(list []domain.Automation, id uuid.UUID) bool {
	for _, a := range list {
		if a.ID == id {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run the repo integration tests**

Run: `go -C backend test -tags=integration ./test/integration/ -run TestAutomationRepository -v`
Expected: PASS (testcontainers spin up Postgres+Redis; migration 000002 applies).

- [ ] **Step 3: Write the end-to-end pipeline test**

Append to `backend/test/integration/automation_test.go`:

```go
import (
	// add to the existing import block:
	"net/http"
	"net/http/httptest"

	"github.com/assafbh/identityhub/internal/automation"
	"github.com/assafbh/identityhub/internal/automation/discover"
	"github.com/assafbh/identityhub/internal/automation/scrape"
	"github.com/assafbh/identityhub/internal/automation/summarize"
	redisstore "github.com/assafbh/identityhub/internal/storage/redis"
)

// captureTickets is a TicketCreator that records calls (stands in for Jira).
type captureTickets struct{ created []domain.TicketPayload }

func (c *captureTickets) CreateTicket(_ context.Context, _ domain.Identity, p domain.TicketPayload) (domain.TicketRef, error) {
	c.created = append(c.created, p)
	return domain.TicketRef{Provider: "jira", IssueKey: "NHI-1", URL: "http://j/NHI-1"}, nil
}

func TestAutomation_EndToEnd(t *testing.T) {
	ctx := context.Background()

	// A fake blog: sitemap with one post + the post HTML.
	mux := http.NewServeMux()
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		_, _ = w.Write([]byte(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>` + base + `/blog/post-1</loc><lastmod>2026-01-01</lastmod></url>
</urlset>`))
	})
	mux.HandleFunc("/blog/post-1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>Post One</title></head><body><article>
		<h1>Post One</h1><p>Paragraph of meaningful body content long enough for extraction.
		Paragraph of meaningful body content long enough for extraction.</p></article></body></html>`))
	})
	// A fake Ollama.
	mux.HandleFunc("/api/generate", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"response":"A short summary."}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	repo := store.NewPostgresAutomationRepository(appPool)
	seen := redisstore.NewRedisAutomationSeenSet(redisClient)
	tickets := &captureTickets{}
	svc := automation.NewService(automation.Deps{
		Repo:           repo,
		Disc:           discover.New(5 * time.Second),
		Scraper:        scrape.New(5 * time.Second),
		Summ:           summarize.New(srv.URL, "qwen2.5:0.5b", 5*time.Second, 8000),
		Tickets:        tickets,
		Seen:           seen,
		MaxPostsPerRun: 5,
	})

	tenantID, userID := seedTenantUser(t, ctx)
	a := domain.Automation{
		ID: uuid.New(), TenantID: tenantID, OwnerUserID: userID,
		SiteURL: srv.URL + "/blog", ProjectKey: "NHI", Provider: domain.ProviderJira,
	}

	// First run files exactly one ticket.
	require.NoError(t, svc.RunOnce(ctx, a))
	require.Len(t, tickets.created, 1)
	require.Equal(t, "Post One", tickets.created[0].Title)
	require.Contains(t, tickets.created[0].Description, "A short summary.")
	require.Contains(t, tickets.created[0].Description, "/blog/post-1")

	// Second run files nothing new (seen-set dedupes).
	require.NoError(t, svc.RunOnce(ctx, a))
	require.Len(t, tickets.created, 1)

	// cleanup seen set
	require.NoError(t, seen.Clear(ctx, a.ID))
}
```

> Merge the two `import` blocks into a single block at the top of the file (Go allows only one per file is conventional; combine them). Keep `//go:build integration` as the first line.

- [ ] **Step 4: Run the e2e test**

Run: `go -C backend test -tags=integration ./test/integration/ -run TestAutomation_EndToEnd -v`
Expected: PASS — one ticket on first run, none on the second.

- [ ] **Step 5: Run the full integration suite**

Run: `task test-integration`
Expected: all integration tests PASS (existing + new).

- [ ] **Step 6: Commit**

```bash
git add backend/test/integration/automation_test.go
git commit -m "test(automation): repo, RLS, and end-to-end integration tests"
```

---

## Task 16: Full verification

**Files:** none (verification only).

- [ ] **Step 1: Format + vet + unit tests (race)**

Run: `task fmt && task test`
Expected: no diffs from fmt; vet clean; all unit tests PASS with `-race`.

- [ ] **Step 2: Integration tests**

Run: `task test-integration`
Expected: PASS.

- [ ] **Step 3: Frontend build**

Run: `cd frontend && npm run build`
Expected: PASS.

- [ ] **Step 4: Full stack smoke (manual, optional)**

```bash
cp .env.example .env   # set CRYPTO_TOKEN_KEY via: openssl rand -base64 32
task up
```
Then: log in, open the **Automation** tab, create an automation pointing at a blog with a `sitemap.xml` (interval 1 minute), connect Jira, click **Run now**, and confirm a ticket appears in the project (and in the Findings → Recent tickets view). Check `docker compose logs scheduler` and `docker compose logs ollama` for the run.

- [ ] **Step 5: Final commit (if any fmt changes)**

```bash
git add -A
git commit -m "chore: format + final verification for automation feature"
```

---

## Notes for the implementer

- **One Go file per code block.** Where a task says "replace the whole file", ignore the illustrative first block (Task 10 has one) and use only the final block.
- **Keep the build green between tasks** by doing Task 11 and Task 12 back-to-back (Task 11 references the `RouterDeps.Automation` field added in Task 12).
- **Library API drift** (Task 7): `go-readability` and `html-to-markdown/v2` APIs are pinned by the tests; if a signature differs from the plan, adapt the call — the test asserts the behavior contract, not the exact API.
- **Memory limits** are enforced in compose (`mem_limit` + `deploy.resources.limits.memory`) on `ollama` and `scheduler`; serial post processing in `RunOnce` ensures only one Ollama inference runs at a time.
