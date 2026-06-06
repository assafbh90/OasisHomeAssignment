# IdentityHub — NHI → Jira Integration

A multi-tenant backend + decoupled SPA that lets an authenticated user connect
their **own Jira Cloud** workspace (OAuth 2.0 3LO) and file **NHI finding
tickets** — from the UI and from a REST API guarded by an API key.

This is a proof-of-concept built for clarity, security, and operability. It is a
**stateless, horizontally-scalable Go monolith** with clean hexagonal package
seams (each is extraction-ready into its own service) plus a separate React SPA.

---

## Quickstart

Prerequisites: Docker + Docker Compose, and [`go-task`](https://taskfile.dev)
(`go install github.com/go-task/task/v3/cmd/task@latest`).

```bash
cp .env.example .env
# Generate a 32-byte token-encryption key:
#   openssl rand -base64 32   -> paste into CRYPTO_TOKEN_KEY
# (Optional now, required for Jira) fill JIRA_CLIENT_ID / JIRA_CLIENT_SECRET.

task up                      # builds & starts frontend, backend, postgres, redis, migrate, seed
```

Then open **http://localhost:3000** and sign in with the seeded first account
(configured by the `SEED_*` variables in `.env`; defaults shown). Login is by
email + password — the tenant is derived from the matched user.

| Field | `.env` var | Default |
|---|---|---|
| Email | `SEED_USER_EMAIL` | `admin@acme.test` |
| Password | `SEED_USER_PASSWORD` | `password123` |

`task down` tears everything down (and volumes). Other handy tasks: `task test`,
`task test-integration`, `task lint`, `task migrate`, `task run`, `task --list`.

> The stack boots without Jira credentials so you can explore auth and the UI;
> the Jira integration returns a clear error until `JIRA_CLIENT_ID/SECRET` are set.

---

## Configuring Jira (3LO)

1. Create an OAuth 2.0 (3LO) app at
   <https://developer.atlassian.com/console/myapps/>. Add the **Jira API**
   permission with scopes `read:jira-work`, `write:jira-work`, `read:jira-user`,
   `offline_access`.
2. Set the **callback URL** to `http://${PUBLIC_HOST}/v1/integrations/jira/callback`.
   `PUBLIC_HOST` is the public authority (host[:port]) and defaults to
   `localhost:${FRONTEND_PORT}` (i.e. `localhost:3000`), so local dev only needs
   the port; in prod set a bare domain. The backend derives `JIRA_REDIRECT_URI`
   from it, so the public origin and the OAuth callback can't drift — set it in
   one place and register the matching URL here. (Set `JIRA_REDIRECT_URI`
   explicitly to fully override the scheme/path too, e.g. https.)
3. Copy the **Client ID/Secret** into `.env` (`JIRA_CLIENT_ID`,
   `JIRA_CLIENT_SECRET`) and `task up` again. In the UI, click **Connect Jira**.

---

## Architecture

### Two subsystems behind one API, joined by `Identity`

The auth middleware resolves the caller from a **session cookie** or a **Bearer
API key**, builds an `Identity{UserID, TenantID, Scopes, AuthMethod}`, and injects
it into the request context. **Every protected handler — including all
integration endpoints — reads tenant/user from `Identity`, never from request
input.** That value object is the seam between the auth and integration code.

```
browser ─┐                             ┌─ auth:        sessions, users, API keys
         ├─ SPA (nginx, proxies /v1)   │
scanner ─┘   └─ REST ─ api ──Identity──┼─ integration: OAuth, encrypted tokens, reactive refresh
                                       └─ finding:     create ticket, recent-tickets view
```

### Hexagonal, dependency-inverted

Interfaces are **consumer-defined** (each feature package declares the small
interface it needs); concrete adapters live in `storage/postgres`,
`storage/redis`, `integration/oauth`, `integration/client`. Wiring happens
**only** in the composition root (`cmd/api/main.go`). The domain is pure Go.

```
backend/internal/
  domain/        # Identity, value objects, sentinel errors (pure)
  auth/          # Argon2id hasher (alexedwards/argon2id) + user authenticator
  session/       # opaque server-side sessions (Redis)
  apitoken/      # OUR machine API keys (hash in PG + Redis cache)
  integration/   # outbound connection lifecycle (connect/callback/disconnect)
    oauth/       # JiraOAuthProvider — authentication (3LO authorize/exchange/refresh)
    client/      # JiraClient — operations on Jira (create issue, list projects)
    oauthtoken/  # ReactiveTokenManager + AES-256-GCM TokenCipher (PROVIDER tokens)
  ticketreport/  # NHI business: report finding as ticket, recent tickets, created_tickets
  transport/http # gin router, middleware, handlers, DTOs
  storage/{postgres,redis}, platform, config, logging
```

### Two distinct "token worlds" (kept apart by design)

| | What | Where | Package |
|---|---|---|---|
| **API keys (PATs)** | *Our* tokens for scanners/CI | SHA-256 hash in Postgres + Redis cache | `internal/apitoken` |
| **OAuth provider tokens** | *Jira's* access/refresh tokens | AES-256-GCM encrypted in Postgres | `internal/integration/oauthtoken` |

They have separate lifecycles, storage, and revocation — never conflated.

### Scalability

App instances are **stateless**; all shared state (sessions, token cache,
rate-limit counters, OAuth state, pending actions) lives in **Redis**, so you can
run N replicas behind a load balancer. Validated API keys are Redis-cached to
avoid a DB hit per request. Postgres uses a tuned `pgxpool`. Graceful shutdown
drains in-flight requests for clean rolling deploys.

---

## Key design decisions (and why)

- **Opaque, server-side sessions over JWT-in-cookie.** Session IDs are 256-bit
  random tokens; all state is in Redis with a sliding TTL bounded by an absolute
  lifetime. This makes sessions **instantly revocable** and leaks nothing into the
  cookie. Cookies are `HttpOnly`, `SameSite=Lax` (so the OAuth callback navigation
  carries them), `Secure` behind TLS.
- **Reactive (lazy) token refresh — no background warmer.** A Jira access token is
  refreshed only when expired at use time, and the rotated refresh token is
  persisted. If the *refresh* token is provably dead (inactivity window passed) or
  the provider rejects it (`invalid_grant`), the credential is flipped to
  `needs_reauth` and the client gets a first-class `409 reauth_required` telling it
  to reconnect — instead of a doomed API call or a retry storm. (A cross-replica
  single-flight on concurrent refreshes — e.g. a `SELECT … FOR UPDATE` row lock —
  is the natural next step; it's intentionally left out of this PoC for simplicity.)
- **Defense-in-depth multi-tenancy.** (1) Middleware resolves tenant only from the
  session/token. (2) Every repository query filters by `tenant_id` and is passed
  the tenant explicitly. (3) **Postgres Row-Level Security** denies any row whose
  `tenant_id` ≠ the per-transaction `app.tenant_id` GUC. The app connects as a
  **non-superuser** role so RLS actually applies; migrations/admin use the
  superuser. The one pre-tenant lookup (API-key-by-hash) goes through a narrow
  `SECURITY DEFINER` function rather than weakening the policy.
- **Secrets handled carefully.** Passwords: Argon2id (~64 MiB, t=3, p=4) via the
  maintained `alexedwards/argon2id`. Jira
  tokens: AES-256-GCM at rest (random nonce per encryption, tamper-detected). API
  keys: only the SHA-256 hash is stored; the plaintext is shown **once**.
  Constant-time comparisons for secrets. OAuth `state` is one-time, bound to
  `{tenant,user}`, **and** cross-checked against the session identity on callback
  (defends the "callback bound to the wrong user" class of attack); PKCE (S256) is
  used. Rate limiting on login. `pprof` runs on a separate internal port. Secrets
  never appear in logs (only token IDs/prefixes and tenant/user IDs).

---

## REST API (for scanners / CI)

Issue an API key in the UI (**API keys → Manage**), then:

```bash
curl -X POST http://localhost:3000/v1/integrations/jira/tickets \
  -H "Authorization: Bearer ih_pat_xxxxx" \
  -H "Content-Type: application/json" \
  -d '{"project_key":"NHI","title":"Stale Service Account: svc-deploy-prod","description":"Detected unused service account."}'
```

| Method & path | Auth | Purpose |
|---|---|---|
| `POST /v1/auth/login` | public | create session |
| `POST /v1/auth/logout` | session | revoke session |
| `GET /v1/auth/me` | session \| key | current identity |
| `POST /v1/tokens` | session | issue API key (plaintext once) |
| `GET/DELETE /v1/tokens[/{id}]` | session \| key | list / revoke keys |
| `GET /v1/integrations/jira/connect` | session | start OAuth |
| `GET /v1/integrations/jira/callback` | session | finish OAuth |
| `GET /v1/integrations/jira/status` | session \| key | connection status |
| `GET /v1/integrations/jira/projects` | session \| key | list projects |
| `POST /v1/integrations/jira/tickets` | session \| key:`integrations:write` | create finding |
| `GET /v1/integrations/jira/tickets?project=KEY` | session \| key | recent tickets |
| `DELETE /v1/integrations/jira` | session \| key:`integrations:write` | disconnect |
| `GET /healthz`, `/readyz` | public | liveness / readiness |

Errors use a uniform envelope; a `409 reauth_required` signals the integration
must be reconnected (call `/connect` again).

---

## Adding another provider (3 steps)

The core (`integration`, `ticketreport`, `transport`) depends only on small
consumer-defined ports (the auth provider and the operations `Client`). To add
e.g. GitHub:

1. Implement `XxxOAuthProvider` in `integration/oauth` and `XxxClient` in
   `integration/client`.
2. Add its config block.
3. Register it in the composition root.

No changes to the orchestration, token manager, or transport. (This PoC ships
Jira only by choice; the seams are there.)

---

## Testing

```bash
task test               # unit tests, table-driven + AAA, race detector
task test-integration   # end-to-end against real Postgres + Redis (testcontainers), Jira mocked
```

Integration tests cover: login→session→logout; API-key issue→use→revoke; the full
Jira flow (connect→callback with state+identity cross-check→**encrypted**
credential→create ticket→recent tickets→disconnect); the reauth/reconnect flow;
and multi-tenant isolation proven at the repository **and** RLS layers.

---

## Assumptions & scope

- **Login is by email + password**; email is globally unique and the tenant is
  derived from the matched user. The pre-tenant lookup uses a narrow
  `SECURITY DEFINER` function (`find_user_for_login`) — the same pattern as the
  API-key-by-hash lookup — so RLS stays enforced for everything else. Demo org
  `acme` + user are seeded.
- **"Recent tickets"** lists tickets *created through IdentityHub* (tracked in
  `created_tickets`), per project — not a live Jira search.
- Single role model (scopes only, no RBAC) for the PoC.
- Tests mock Jira via base-URL override; local/real runs use a Jira 3LO app.
- The default deployment serves the SPA same-origin via the frontend's nginx
  proxy, so session cookies work without CORS (CORS-with-credentials is supported
  as an alternative via `HTTP_ALLOWED_ORIGINS`).
- **NHI Automation (blog digest).** The bonus is implemented as a generalized
  *Automation* tab: watch a site URL; a separate `scheduler` container periodically
  discovers new posts via `sitemap.xml`, scrapes each to markdown, summarizes it
  with a local, memory-limited Ollama model (`qwen2.5:0.5b`, configurable), and
  files a Jira ticket per post into a chosen project. Automations are tenant-shared
  and use the creator's Jira credential; processed URLs are tracked in Redis so each
  post is filed once. See `docs/superpowers/specs/2026-06-06-nhi-automation-blog-digest-design.md`.
