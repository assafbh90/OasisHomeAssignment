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
