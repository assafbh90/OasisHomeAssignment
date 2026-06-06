# IdentityHub — Data Model Reference

A complete, walk-through-ready reference for **where every piece of state lives**
and **why**. Two stores, with a deliberate split:

| Store | Holds | Why it lives here |
|---|---|---|
| **Postgres** | Durable, relational, tenant-owned records (users, API keys, encrypted Jira credentials, automations) | Needs ACID, foreign keys, and Row-Level Security for tenant isolation |
| **Redis** | Ephemeral / derived / high-churn state (sessions, caches, rate-limit counters, OAuth handshake state, locks, dedupe sets) | Fast, TTL-native, and lets the API run as **N stateless replicas** — no shared state in the app process |

> **Design rule:** anything that must survive a restart and belongs to a tenant
> goes in Postgres under RLS. Anything that is a cache, a counter, a lock, or a
> short-lived handshake goes in Redis with a TTL. There is **no tickets table** —
> the "recent tickets" view is a Redis cache rebuilt from Jira, which is the
> source of truth.

---

## Part 1 — Postgres

Single database. Multi-tenancy is enforced **in three layers**:

1. **Middleware** resolves `tenant_id` only from the authenticated session/API key
   (never from request input).
2. **Every repository query** filters by `tenant_id` explicitly.
3. **Row-Level Security (RLS)** — Postgres itself denies any row whose `tenant_id`
   ≠ the per-transaction GUC `app.tenant_id`. This is the backstop if a query ever
   forgets layer 2.

The app connects as a **non-superuser role** (`identityhub_app`) so RLS actually
applies to it; migrations/admin/tests run as the superuser, which bypasses RLS by
design.

### How RLS works (the mechanism)

```sql
-- Reads the per-transaction GUC; returns NULL when unset → policy matches no rows.
CREATE FUNCTION app_current_tenant() RETURNS UUID
  AS $$ SELECT NULLIF(current_setting('app.tenant_id', true), '')::uuid $$;

-- Applied to users, api_tokens, integration_credentials, automations:
CREATE POLICY tenant_isolation ON <table>
  USING       (tenant_id = app_current_tenant())   -- which rows are visible
  WITH CHECK  (tenant_id = app_current_tenant());   -- which rows may be written
```

The repository layer opens a transaction and sets the GUC **transaction-locally**
before any statement:

```go
// db.go — inTenantTx
tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID.String())
//                                                      ^^^^ true = tx-scoped, auto-reset
```

So `tenant_id` is bound to the connection only for the life of that transaction —
no leakage across pooled connections.

**Deny-by-default:** when the GUC is unset, `app_current_tenant()` is NULL and the
policy matches **zero** rows.

### Pre-tenant lookups (the RLS escape hatches)

Three operations happen **before** any tenant context exists, so they can't be
filtered by `app.tenant_id`. Instead of weakening the RLS policies, each is a
narrow `SECURITY DEFINER` function (runs as the table owner → bypasses RLS for
that one read), granted to the app role:

| Function | Used by | Returns |
|---|---|---|
| `find_user_for_login(email)` | login (email is globally unique) | 0/1 user |
| `find_api_token_by_hash(hash)` | per-request API-key auth | 0/1 token |
| `claim_due_automations(limit, lease)` | scheduler (cross-tenant) | due rows, atomically marked `running` |

---

### Enums

```sql
connection_status = { 'connected', 'needs_reauth', 'revoked' }   -- integration_credentials.status
automation_status = { 'idle', 'running' }                        -- automations.status
```

`needs_reauth` is what the reactive token manager flips a credential to when the
refresh token is dead → the API surfaces `409 reauth_required` → the UI prompts a
reconnect.

---

### Tables

#### `tenants` — organizations *(no RLS)*

Reference data, read **before** tenant context exists (login resolves a tenant
from the matched user). Not under RLS because there is no tenant to scope by yet.

| Column | Type | Notes |
|---|---|---|
| `id` | UUID PK | `gen_random_uuid()` |
| `slug` | TEXT **UNIQUE** | stable external handle (e.g. `acme`) |
| `name` | TEXT | |
| `created_at` / `updated_at` | TIMESTAMPTZ | |

**Indexes:** `UNIQUE(slug)`.

---

#### `users` — login identities *(RLS ✓)*

| Column | Type | Notes |
|---|---|---|
| `id` | UUID PK | |
| `tenant_id` | UUID FK → tenants | `ON DELETE CASCADE` |
| `email` | TEXT **UNIQUE (global)** | login is by email alone → tenant derived from the match |
| `password_hash` | TEXT | **Argon2id** (~64 MiB, t=3, p=4) |
| `status` | TEXT | default `active` |
| `created_at` / `updated_at` | TIMESTAMPTZ | |

**Indexes:** `UNIQUE(email)` (serves both login and seed lookup);
`idx_users_tenant_fk (tenant_id)` — Postgres does **not** auto-index FK columns,
and an unindexed FK forces a seq-scan + stronger lock on cascade deletes.

---

#### `api_tokens` — *our* machine keys (PATs) *(RLS ✓)*

The keys scanners/CI use to call the REST API. **Only the hash is stored** — the
plaintext (`ih_pat_…`) is shown once at issue time and never again.

| Column | Type | Notes |
|---|---|---|
| `id` | UUID PK | |
| `tenant_id` | UUID FK → tenants | cascade |
| `owner_id` | UUID FK → users | cascade |
| `name` | TEXT | human label |
| `token_hash` | BYTEA **UNIQUE** | **SHA-256** of the token; the auth lookup key |
| `prefix` | TEXT | leading plaintext chars, shown in the UI to identify a key |
| `scopes` | TEXT[] | e.g. `{integrations:write}` |
| `expires_at` / `last_used_at` / `revoked_at` | TIMESTAMPTZ NULL | lifecycle |
| `created_at` | TIMESTAMPTZ | |

**Indexes:**
- `UNIQUE(token_hash)` — also serves the per-request lookup-by-hash (no separate index needed).
- `idx_api_tokens_owner_recent (tenant_id, owner_id, created_at DESC)` — one composite serves the "list my keys, newest-first" filter **and** sort (no sort step), and its leading `tenant_id` covers the tenants FK cascade.
- `idx_api_tokens_owner_fk (owner_id)` — for the users FK cascade (not the leading column above).

---

#### `integration_credentials` — encrypted Jira (OAuth) tokens *(RLS ✓)*

One row per `(tenant, user, provider)`. Holds *Jira's* tokens — distinct from our
API keys (different lifecycle, storage, and reversibility).

| Column | Type | Notes |
|---|---|---|
| `id` | UUID PK | |
| `tenant_id` | UUID FK → tenants | cascade |
| `user_id` | UUID FK → users | cascade |
| `provider` | TEXT | `jira` |
| `access_token` | BYTEA | **AES-256-GCM ciphertext** (`nonce‖ciphertext‖tag`) |
| `refresh_token` | BYTEA | **AES-256-GCM ciphertext** |
| `scopes` | TEXT[] | granted OAuth scopes |
| `external_account_id` | TEXT | Jira **cloudid** |
| `site_url` | TEXT | e.g. `https://acme.atlassian.net` (for building issue links) |
| `access_expires_at` | TIMESTAMPTZ | drives reactive refresh |
| `refresh_last_used_at` | TIMESTAMPTZ | inactivity window → detect a dead refresh token |
| `status` | `connection_status` | `connected` / `needs_reauth` / `revoked` |
| `created_at` / `updated_at` | TIMESTAMPTZ | |

**Constraint/Indexes:** `UNIQUE(tenant_id, user_id, provider)` — every read and the
single-flight `SELECT … FOR UPDATE` filter by exactly these columns, so the row
lock is taken through this index (one indexed row, never a table scan). Its leading
`tenant_id` also covers the tenants FK cascade. `idx_cred_user_fk (user_id)` covers
the users FK cascade.

> **Why encrypted, not hashed?** We must *recover* these tokens in plaintext to
> call Jira, so they need reversible **encryption** (AES-256-GCM, authenticated:
> tampering fails closed). Our own API keys we only ever *compare*, so they're
> one-way **SHA-256 hashes**. Encryption happens entirely inside the credential
> repository — the rest of the app sees plaintext `domain.Credential`.

---

#### `automations` — blog-digest watchers *(RLS ✓)*

Tenant-**shared** (any member can see/edit), but each pins an `owner_user_id`
whose stored Jira credential the scheduler uses.

| Column | Type | Notes |
|---|---|---|
| `id` | UUID PK | |
| `tenant_id` | UUID FK → tenants | cascade |
| `owner_user_id` | UUID FK → users | cascade; whose Jira credential the worker uses |
| `name` | TEXT | |
| `site_url` | TEXT | the watched blog/site (e.g. `https://oasis.security/blog`) |
| `provider` | TEXT | default `jira` |
| `project_key` | TEXT | where tickets are filed |
| `interval_seconds` | INTEGER | scan cadence |
| `enabled` | BOOLEAN | default `true` |
| `status` | `automation_status` | `idle` / `running` (the claim flips idle→running) |
| `next_scan_at` | TIMESTAMPTZ | due when `<= now()`; advances by interval **on completion** |
| `locked_at` | TIMESTAMPTZ NULL | lease stamp; a crashed run self-heals once `now() - locked_at > lease` |
| `last_run_at` / `last_error` | TIMESTAMPTZ / TEXT NULL | surfaced in the UI (e.g. "no sitemap.xml found") |
| `created_at` / `updated_at` | TIMESTAMPTZ | |

**Indexes:**
- `idx_automations_due (next_scan_at) WHERE enabled` — a **partial** index serving the due-row scan (only enabled rows, pre-sorted by the ordering column).
- `idx_automations_tenant_fk (tenant_id)`, `idx_automations_owner_fk (owner_user_id)` — FK cascades.

**The claim (exactly-once-ish, crash-safe):**

```sql
-- claim_due_automations(limit, lease): SECURITY DEFINER, cross-tenant
UPDATE automations SET status='running', locked_at=now()
WHERE id IN (
  SELECT id FROM automations
  WHERE enabled AND next_scan_at <= now()
    AND (status='idle' OR (status='running' AND locked_at < now() - lease))  -- reclaim crashed
  ORDER BY next_scan_at
  FOR UPDATE SKIP LOCKED      -- two schedulers never grab the same row
  LIMIT limit
) RETURNING *;
```

---

### Roles & privileges

```sql
CREATE ROLE identityhub_app NOLOGIN;   -- created in migration, no secret in SQL
GRANT USAGE ON SCHEMA public TO identityhub_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES ... TO identityhub_app;
GRANT EXECUTE ON FUNCTION find_user_for_login, find_api_token_by_hash, claim_due_automations;
```

`LOGIN` + password are granted **out-of-band** after migration (the compose
`migrate` step / test setup), so no DB credential is ever committed. The role is a
**non-owner non-superuser** specifically so RLS applies to it.

### Entity relationships

```text
  tenants ──1:N──┬── users ──1:N──┬── api_tokens               (SHA-256 hash, scopes)
                 │                 ├── integration_credentials  (AES-GCM tokens, one per provider)
                 │                 └── automations (owner)      (watched site + schedule + run state)
```

---

## Part 2 — Redis

All keys are **flat** (prefix + id). Everything has a TTL **except** the two index
sets (`usersessions:` and `automation:seen:`), which are cleaned up explicitly.

| Key pattern | Type | TTL | Written by | Purpose |
|---|---|---|---|---|
| `session:{sessionID}` | string (JSON `SessionData`) | sliding `SESSION_TTL` (def **24h**), capped by `SESSION_ABSOLUTE_TTL` (def **168h**) | `RedisSessionStore` | the actual session record |
| `usersessions:{userID}` | **SET** of session IDs | absolute lifetime **+ 1h** | `RedisSessionStore` | reverse index → revoke *all* of a user's sessions |
| `tokencache:{hexSHA256(apiKey)}` | string (JSON identity) | `API_TOKEN_CACHE_TTL` (def **60s**) | `RedisTokenCache` | cache validated API keys → skip a DB hit per request |
| `oauthstate:{state}` | string (JSON: tenant, user, PKCE verifier) | **10m** (`oauthStateTTL`) | `RedisOAuthStateStore` | one-time OAuth handshake state (read via `GETDEL`) |
| `tickets:{tenantID}` | string (JSON list, capped **200**) | **24h** (`ticketCacheTTL`) | `RedisTicketCache` | the "recent tickets" view (derived from Jira) |
| `reconcile:last:{tenantID}` | string `"1"` | **30m** (`reconcileWindow`) | `RedisReconcileGate` | throttle marker — skip reconcile if one ran recently |
| `reconcile:lock:{tenantID}` | string (token) | **2m** (`reconcileLockTTL`) | `RedisReconcileGate` | single-flight lock (`SET NX`) — collapse a burst |
| `ratelimit:login:ip:{ip}` | GCRA internal (redis_rate) | per-window | `RedisRateLimiter` | login rate limit, per source IP |
| `ratelimit:login:acct:{email}` | GCRA internal (redis_rate) | per-window | `RedisRateLimiter` | login rate limit, per account |
| `automation:seen:{automationID}` | **SET** of URLs | none (cleared on delete/run-reset) | `RedisAutomationSeenSet` | dedupe — a post is filed once |

### Notes per structure

**Sessions (`session:` + `usersessions:`).**
Opaque 256-bit random session IDs; **all** session state lives in Redis (nothing in
the cookie). The cookie is `HttpOnly`, `SameSite=Lax`, `Secure` behind TLS. TTL is
**sliding** (each use calls `EXPIRE` to extend by `SESSION_TTL`) but bounded by an
absolute lifetime. `usersessions:{userID}` is a SET indexing every live session for
a user, so logout-everywhere / forced revocation deletes them all in one pass. This
is why sessions are **instantly revocable** — unlike a stateless JWT-in-cookie.

**API-key cache (`tokencache:`).**
Keyed by the **hex SHA-256** of the presented Bearer token (same value as the DB
`token_hash`, so the plaintext is never the key). A 60s TTL means a revoked key
stops working within a second-ish without a DB read on the hot path.

**OAuth state (`oauthstate:`).**
Stores `{tenant, user, PKCE code_verifier}` for the duration of the Atlassian
consent redirect. Consumed with `GETDEL` so it's strictly **one-time**, and the
`{tenant,user}` inside is cross-checked against the session identity on callback
(defends the "callback bound to the wrong user" mix-up class). 10m = how long a
user has to click through consent.

**Ticket cache (`tickets:`).**
The only "data" cache. A per-tenant JSON list of `CreatedTicket`, capped at the 200
newest, TTL 24h. `Add` prepends on create; `Replace` rebuilds it from a Jira label
search (`labels = identityhub`) during reconcile. `ListByProject` filters this list
by project and returns the newest `limit` (the UI asks for **10**). **Jira is the
source of truth** — this cache self-heals from it, which is why there's no Postgres
tickets table and why it naturally surfaces tickets created by *other* tenant users
or before the app existed.

**Reconcile gate (`reconcile:last:` + `reconcile:lock:`).**
Two keys implement "refresh the ticket cache, but not too often and not twice at
once":
- `reconcile:last:{tenant}` exists → a reconcile ran within the **30m** window → skip (unless `force`, i.e. the manual Refresh button).
- `reconcile:lock:{tenant}` via `SET NX` (2m) → only one reconcile per tenant runs at a time; a burst of connects collapses to one Jira search.

**Rate limit (`ratelimit:`).**
GCRA token-bucket via `redis_rate` (smoother than a fixed window — no
burst-at-boundary flaw), keyed **both** per-IP (`login:ip:{ip}`) and per-account
(`login:acct:{email}`) so neither a single IP nor a single targeted account can be
hammered. On block, a `Retry-After` header is returned with `429`.

**Automation seen-set (`automation:seen:`).**
A SET of already-processed post URLs per automation. The pipeline does
`new = discovered − SMEMBERS(seen)`, and a URL is `SADD`-ed **only after its ticket
is successfully created** — so a failed post retries next run (exactly-once-ish,
self-healing). Deleting an automation `DEL`s its set. No TTL: it's durable dedupe
state, not a cache.

---

## Why this split scales

The API process holds **no** state — sessions, caches, counters, locks, and OAuth
handshakes are all in Redis; durable records are in Postgres. So you can run
`docker compose up --scale backend=N` behind a load balancer and any replica can
serve any request. The slow/heavy automation pipeline runs in a **separate
scheduler container** (same binary, `scheduler` subcommand) that claims due rows
from Postgres with `FOR UPDATE SKIP LOCKED` — so multiple schedulers are safe too.
