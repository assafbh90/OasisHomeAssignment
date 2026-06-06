# Jira Ticket Tagging + Drift Reconciliation — Design

_Date: 2026-06-06_

## Goal

Make IdentityHub-created Jira tickets easy to find and keep our "recent tickets"
view honest against Jira:

1. **Tag** every ticket IdentityHub creates with an `identityhub` Jira label.
2. **Discover** all `identityhub`-labelled tickets on the connected Jira site —
   including ones created by *other* users of the same tenant, and ones that
   already existed before a fresh start.
3. **Reconcile drift** so the view matches Jira, without a fixed cron.

## Key decisions

- **Jira is the source of truth.** Our stored list is a *cache* of the Jira
  label search, not an authoritative record.
- **The cache lives in Redis, keyed per tenant** (`tickets:{tenantID}` → JSON
  list, TTL ~24h, capped to the latest 200). The Postgres `created_tickets`
  table is **removed** (table, RLS policy, indexes, `PostgresTicketRepository`).
  Rationale: it's now a pure cache; Redis is the right tool, reconcile becomes an
  atomic whole-set replace, and an idle tenant's cache expires and self-heals on
  the next connect. The data is non-secret (Jira issue metadata), so dropping the
  Postgres RLS layer for it is acceptable; sensitive data (sessions, credentials,
  API keys) stays in Postgres/RLS.
- **One Jira site per tenant** (documented assumption). Reconcile uses the
  triggering user's credential to search that site; the cache key is the tenant.
- **No background job.** Reconcile is triggered by usage:
  - **on connect** (OAuth callback success) → async, throttled.
  - **refresh button** (`POST /v1/integrations/jira/reconcile`) → forced, sync.
- **Concurrency:** a Redis gate makes a burst of simultaneous connects collapse
  to at most one reconcile: a throttle (`reconcile:last:{tenant}`, ~30m) plus a
  single-flight lock (`reconcile:lock:{tenant}`, `SET NX EX`). Idleness is free —
  no triggers ⇒ nothing runs.
- **Deletion semantics:** the cache mirrors the latest Jira search exactly, so a
  ticket deleted/unlabelled in Jira simply isn't in the new set (atomic replace).

## Components

### domain
- `IdentityHubLabel = "identityhub"` (business constant).
- `ProviderTicket{IssueKey, Title, ProjectKey, URL, CreatedAt}` — a search result.
- `CreatedTicket` reduced to a cache entry: `{TenantID, Provider, ProjectKey,
  IssueKey, IssueURL, Title, CreatedAt}` (drop `ID`, `UserID`).

### integration/client (JiraClient)
- `CreateIssue` always appends `IdentityHubLabel` to the issue's labels.
- `SearchByLabel(ctx, auth) ([]domain.ProviderTicket, error)` — JQL
  `labels = "identityhub" ORDER BY created DESC`, paginated, bounded to 200,
  browse URL from the site URL.

### storage/redis
- `RedisTicketCache` (`tickets:{tenant}`, TTL): `Replace`, `Add` (best-effort
  append, newest-N), `ListByProject`.
- `RedisReconcileGate`: `Begin(ctx, tenantID, force) (proceed bool, finish func(), err error)`
  — throttle + single-flight lock; `finish` stamps the throttle key and releases.

### ticketreport.Service
Ports: `Client` (now also `SearchByLabel`), `TicketCache`, `ReconcileGate`,
plus existing `TokenManager`, `CredentialReader`.
- `CreateTicket` → create in Jira (tagged) → `cache.Add` (best-effort) → ref.
- `ListRecentTickets` → `cache.ListByProject`.
- `Reconcile(ctx, principal, force)` → `gate.Begin` → (if proceed) resolve token
  + site → `client.SearchByLabel` → map → `cache.Replace`.

### transport/http
- `POST /v1/integrations/jira/reconcile` → `Reconcile(force=true)`; 204 on
  success, 409 if the caller's token needs reauth.
- `Callback` success → `go reports.Reconcile(detached ctx, id, force=false)`.
- `ListRecentTickets` reads the tenant cache (no per-user filter).

## Testing

- client: `SearchByLabel` (JQL params, pagination, mapping) against `httptest`;
  `CreateIssue` always carries the `identityhub` label.
- service: `Reconcile` replaces the cache from the search; gate-skip path; create
  appends to cache.
- redis: `RedisTicketCache` round-trip + cap; `RedisReconcileGate` throttle +
  single-flight (real Redis).
- integration: create→search finds it; fresh tenant discovers a pre-existing
  labelled issue; a ticket removed in Jira drops from the cache after reconcile.

## Out of scope

- Multiple Jira sites per tenant.
- Field sync beyond title/URL/project/created.
- Background/cron reconciliation.
