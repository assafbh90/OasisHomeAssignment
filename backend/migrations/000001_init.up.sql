-- IdentityHub initial schema.
-- Multi-tenancy is enforced in depth: every tenant table carries tenant_id,
-- repositories scope every query by tenant (layer 2), and Row-Level Security
-- (layer 3) denies any row whose tenant_id != the per-transaction GUC
-- `app.tenant_id`. RLS is FORCED so even the table owner (the app role) is
-- subject to it.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TYPE connection_status AS ENUM ('connected', 'needs_reauth', 'revoked');

-- Tenants & users are the identity-bootstrap tables. `tenants` is reference data
-- read before any tenant context exists (login resolves a tenant by slug), so it
-- is not under RLS; `users` is under RLS once the tenant is established.
CREATE TABLE tenants (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug       TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id     UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    email         TEXT NOT NULL UNIQUE,   -- globally unique: login is by email alone
    password_hash TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'active',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- email is UNIQUE (its implicit index serves both login and the seed lookup).
-- tenant_id is indexed for the tenants FK: Postgres does NOT auto-index FK
-- columns, and an unindexed FK forces a seq scan + a stronger lock on this
-- (child) table whenever the referenced tenant row is deleted.
CREATE INDEX idx_users_tenant_fk ON users (tenant_id);

CREATE TABLE api_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    owner_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    token_hash   BYTEA NOT NULL UNIQUE,
    prefix       TEXT NOT NULL,
    scopes       TEXT[] NOT NULL DEFAULT '{}',
    expires_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Lookup-by-hash (the per-request bootstrap read) is already served by the
-- UNIQUE(token_hash) index, so no separate hash index is needed.
-- Listing a user's keys filters (tenant_id, owner_id) and sorts newest-first;
-- one composite serves the filter AND the ORDER BY (no sort step), and its
-- leading tenant_id also covers the tenants FK cascade. owner_id is indexed
-- separately for the users FK cascade (it isn't the leading column above).
CREATE INDEX idx_api_tokens_owner_recent ON api_tokens (tenant_id, owner_id, created_at DESC);
CREATE INDEX idx_api_tokens_owner_fk ON api_tokens (owner_id);

CREATE TABLE integration_credentials (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id            UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    user_id              UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider             TEXT NOT NULL,
    access_token         BYTEA NOT NULL,   -- AES-256-GCM ciphertext
    refresh_token        BYTEA NOT NULL,   -- AES-256-GCM ciphertext
    scopes               TEXT[] NOT NULL DEFAULT '{}',
    external_account_id  TEXT,             -- e.g. Jira cloudid
    site_url             TEXT,             -- e.g. https://acme.atlassian.net (for issue links)
    access_expires_at    TIMESTAMPTZ NOT NULL,
    refresh_last_used_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    status               connection_status NOT NULL DEFAULT 'connected',
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, user_id, provider)
);
-- Every read AND the single-flight `SELECT ... FOR UPDATE` filter by
-- (tenant_id, user_id, provider) — exactly the UNIQUE constraint's index — so
-- the row lock is taken through that index (one indexed row locked, never a
-- table scan that would lock-check unrelated rows). That index (leading
-- tenant_id) also covers the tenants FK cascade, so the only extra index needed
-- is user_id for the users FK cascade. (No status/refresh_last_used index: there
-- is no scan over those columns yet; add a composite if a prune job is added.)
CREATE INDEX idx_cred_user_fk ON integration_credentials (user_id);

-- Note: the "recent tickets" view is a Redis cache of the Jira label search
-- (Jira is the source of truth), so there is no Postgres tickets table.

-- ---------------------------------------------------------------------------
-- Row-Level Security. Policies match rows whose tenant_id equals the
-- per-transaction GUC app.tenant_id (set by the repository layer from the
-- authenticated Identity). When the GUC is unset, current_setting(..., true)
-- returns NULL and the comparison yields no rows: deny by default.
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION app_current_tenant() RETURNS UUID
LANGUAGE sql STABLE AS $$
    SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid
$$;

DO $$
DECLARE t TEXT;
BEGIN
    FOREACH t IN ARRAY ARRAY['users', 'api_tokens', 'integration_credentials']
    LOOP
        EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
        EXECUTE format(
            'CREATE POLICY tenant_isolation ON %I USING (tenant_id = app_current_tenant()) WITH CHECK (tenant_id = app_current_tenant())',
            t);
    END LOOP;
END $$;

-- ---------------------------------------------------------------------------
-- Least-privilege application role. The app connects as this NON-superuser,
-- non-owner role so RLS actually applies to it (superusers and table owners
-- bypass RLS). Migrations and admin tasks use the superuser. The role is
-- created NOLOGIN here (no secret in SQL); LOGIN + password are granted
-- out-of-band after migration (the compose migrate step / test setup), so no
-- credential is ever committed.
-- ---------------------------------------------------------------------------
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'identityhub_app') THEN
        CREATE ROLE identityhub_app NOLOGIN;
    END IF;
END $$;
GRANT USAGE ON SCHEMA public TO identityhub_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO identityhub_app;

-- Auth bootstrap: an API token is looked up by its hash before any tenant
-- context exists, so that single read must bypass RLS. Expose it as a narrow
-- SECURITY DEFINER function (runs as the owner, which bypasses RLS) rather than
-- weakening the table policy. SETOF returns 0 or 1 rows cleanly.
CREATE OR REPLACE FUNCTION find_api_token_by_hash(p_hash BYTEA)
RETURNS SETOF api_tokens
LANGUAGE sql STABLE SECURITY DEFINER AS $$
    SELECT * FROM api_tokens WHERE token_hash = p_hash
$$;
GRANT EXECUTE ON FUNCTION find_api_token_by_hash(BYTEA) TO identityhub_app;

-- Login is by email alone (no tenant context yet), so the user lookup must also
-- bypass RLS. Same narrow SECURITY DEFINER pattern; email is globally unique.
CREATE OR REPLACE FUNCTION find_user_for_login(p_email TEXT)
RETURNS SETOF users
LANGUAGE sql STABLE SECURITY DEFINER AS $$
    SELECT * FROM users WHERE email = p_email
$$;
GRANT EXECUTE ON FUNCTION find_user_for_login(TEXT) TO identityhub_app;
