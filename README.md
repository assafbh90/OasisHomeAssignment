# IdentityHub — NHI → Jira Integration

A multi-tenant backend + decoupled SPA that lets an authenticated user connect
their **own Jira Cloud** workspace (OAuth 2.0 3LO) and file **NHI finding
tickets** — from the UI and from a REST API guarded by an API key.

This is a proof-of-concept built for clarity, security, and operability: a
**stateless, horizontally-scalable Go monolith** with clean hexagonal package
seams (each is extraction-ready into its own service) plus a separate React SPA.

It also ships an **Automation** subsystem (the optional "NHI Blog Digest",
generalized): a tenant watches a blog/site, and a separate scheduler worker
discovers new posts, summarizes each with a **local** LLM (Ollama), and files a
Jira ticket per post — reusing the same Jira client, reactive token manager, and
RLS.

> **NHI** = Non-Human Identity (service accounts, API keys, service principals).
> The product detects identity issues (stale accounts, over-privileged keys,
> expiring credentials); this PoC turns those findings into Jira tickets.

---

## Table of contents

- [Quickstart](#quickstart)
- [Configuring Jira (3LO)](#configuring-jira-3lo)
- [Architecture](docs/ARCHITECTURE.md) — system view · package layout · token worlds · request lifecycles
- [Automation (NHI Blog Digest)](#automation-nhi-blog-digest)
- [Repository structure](#repository-structure)
- [Data model](#data-model) · [full reference](docs/DATA-MODEL.md)
- [Key design decisions](docs/DECISIONS.md)
- [REST API](#rest-api-for-scanners--ci)
- [Configuration reference](#configuration-reference)
- [Adding another provider](#adding-another-provider-3-steps)
- [Testing](#testing)
- [Assumptions & scope](#assumptions--scope)

---

## Quickstart

Prerequisites: Docker + Docker Compose, and [`go-task`](https://taskfile.dev)
(`go install github.com/go-task/task/v3/cmd/task@latest`).

```bash
cp .env.example .env
# Generate a 32-byte token-encryption key:
#   openssl rand -base64 32   -> paste into CRYPTO_TOKEN_KEY
# (Optional now, required for Jira) fill JIRA_CLIENT_ID / JIRA_CLIENT_SECRET.

task up                # builds & starts frontend, backend, postgres, redis, migrate, seed
```

Then open **http://localhost:3000** and sign in with the seeded first account
(configured by the `SEED_*` variables in `.env`; defaults shown). Login is by
email + password — the tenant is derived from the matched user.

| Field | `.env` var | Default |
|---|---|---|
| Email | `SEED_USER_EMAIL` | `admin@acme.test` |
| Password | `SEED_USER_PASSWORD` | `password123` |

`task down` tears everything down (and volumes). Handy tasks (`task --list`):

| Task | What it does |
|---|---|
| `task up` / `task down` | build & start the full stack / stop it and drop volumes |
| `task infra` | start only Postgres + Redis (for local non-Docker backend dev) |
| `task run` | run the backend locally against `task infra` |
| `task test` | unit tests, race detector, `-short` |
| `task test-integration` | end-to-end tests on real Postgres + Redis (testcontainers) |
| `task cover` | combined unit+integration coverage report |
| `task lint` / `task fmt` | golangci-lint / gofmt + go vet |
| `task migrate` / `task migrate-down` | apply / roll back one DB migration |
| `task swag` | regenerate the OpenAPI spec from handler annotations |

> The stack boots **without** Jira credentials so you can explore auth and the UI;
> the Jira integration returns a clear error until `JIRA_CLIENT_ID/SECRET` are set.

---

## Configuring Jira (3LO)

1. Create an OAuth 2.0 (3LO) app at
   <https://developer.atlassian.com/console/myapps/>. Add the **Jira API**
   permission with scopes `read:jira-work`, `write:jira-work`, `read:jira-user`,
   `offline_access`.
2. Set the **callback URL** to `http://${PUBLIC_HOST}/v1/integrations/jira/callback`.
   `PUBLIC_HOST` is the public authority (`host[:port]`) and defaults to
   `localhost:${FRONTEND_PORT}` (i.e. `localhost:3000`), so local dev only needs
   the port; in prod set a bare domain. The backend derives `JIRA_REDIRECT_URI`
   from it, so the public origin and the OAuth callback can't drift — set it in
   one place and register the matching URL here. (Set `JIRA_REDIRECT_URI`
   explicitly to fully override the scheme/path too, e.g. https.)
3. Copy the **Client ID/Secret** into `.env` (`JIRA_CLIENT_ID`,
   `JIRA_CLIENT_SECRET`) and `task up` again. In the UI, click **Connect Jira**.

---

## Architecture

IdentityHub is a **modular monolith** with a **hexagonal / ports-and-adapters**
layout: a stateless, horizontally-scalable Go backend plus a decoupled React SPA.
Interfaces are consumer-defined, concrete adapters live in `storage/*` and
`integration/*`, and wiring happens only in the composition root (`internal/app`).
The two token worlds — *our* API keys (SHA-256) and *Jira's* OAuth tokens
(AES-256-GCM) — are kept strictly apart.

> **Full detail — system view, package layout, the two token worlds, scalability,
> and the per-request lifecycles (auth, OAuth connect, reactive refresh, drift
> reconcile) — is in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).**

---

## Automation (NHI Blog Digest)

The optional "NHI Blog Digest", generalized into a reusable **Automation**
subsystem. A tenant member defines an automation that **watches a blog/site URL**;
on a schedule, a separate worker discovers new posts, summarizes each with a
**local** LLM, and files **one Jira ticket per post** into a chosen project. The
new code is only the automation glue — it reuses `ticketreport.CreateTicket`, the
Jira client, the reactive token manager, RLS, and config/logging unchanged. So
automation-created tickets are tagged `identityhub` and also appear in the
**recent-tickets** view.

```text
  API (backend)                         scheduler worker (separate container, mem-limited)
    CRUD /v1/automations ──► Postgres ◄── claim due rows  (claim_due_automations:
                              automations    SELECT … FOR UPDATE SKIP LOCKED + lease)
                                                 │  per claimed row (tenant-scoped):
                                                 ▼
   discover ───► diff vs Redis seen-set ───► scrape ──────► summarize ────► create Jira ticket
   sitemap.xml   new = urls − SMEMBERS        fetch +        Ollama          reuse ticketreport
   (+ index,     seen:{automation_id}         readability    qwen2.5:0.5b    (also → recent tickets)
   prefix filter,                             → markdown      (mem-limited)        │
   lastmod sort)                                                                   ▼
                                                          on ticket success: SADD seen:{id} url
```

Design choices:

- **Separate `scheduler` worker, not an in-process goroutine.** The pipeline is
  slow and memory-heavy (HTTP fetch + LLM inference); isolating it keeps API
  latency unaffected and lets the worker and Ollama be memory-limited
  independently. It's the **same binary** run with the `scheduler` subcommand
  (like `seed`), sharing the single composition root.
- **Tenant-shared automations, owner's credential.** An automation belongs to a
  tenant (visible/editable by any member) and pins an `owner_user_id` whose stored
  Jira credential the worker uses (via the reactive token manager).
- **Discovery via `sitemap.xml` only** (incl. sitemap-index), filtered to the
  watched prefix, newest-first by `lastmod`. No `<a>`-crawling. No sitemap → clear
  `last_error`.
- **Minimal local scraper** (HTTP → `go-readability` → markdown) and **local
  summarization** via a pre-warmed Ollama image (`qwen2.5:0.5b` pre-pulled at
  build), both memory-limited. No external SaaS, no API keys.
- **Exactly-once-ish, self-healing.** A URL is added to the Redis seen-set
  **only after its ticket is created**, so a failed post retries next run. A row
  is claimed by flipping `idle→running` (no overlapping scans); `next_scan_at`
  advances by the per-automation interval **only on completion**; a `locked_at`
  lease lets a crashed run self-heal. A per-run cap drains a backlog over runs.
- **Clear errors.** `last_error` is surfaced per automation ("no sitemap.xml
  found", "Jira reconnect required", "Ollama timeout"); a `reauth_required` aborts
  the run cleanly (nothing marked seen).

Managed from the **Automation** tab in the UI, or the `/v1/automations` REST
endpoints (see [REST API](#rest-api-for-scanners--ci)).

---

## Repository structure

```text
.
├── backend/
│   ├── cmd/api/main.go                 # thin shell → app.Run() (dispatches: serve | seed | scheduler)
│   ├── internal/
│   │   ├── app/                        # composition root: Build() binds adapters→ports; Serve/Seed/RunScheduler
│   │   ├── domain/                     # Identity, value objects, sentinel errors, scopes, Automation (pure)
│   │   ├── auth/                       # Argon2id hasher (alexedwards/argon2id) + UserAuthenticator
│   │   ├── session/                    # opaque server-side session manager (Redis-backed)
│   │   ├── apitoken/                   # OUR machine API keys: issue / authenticate / list / revoke
│   │   ├── integration/               # outbound connection lifecycle (connect/callback/status/disconnect)
│   │   │   ├── oauth/                  #   JiraOAuthProvider — 3LO authorize/exchange/refresh (x/oauth2)
│   │   │   ├── client/                 #   JiraClient — Jira REST ops (create issue, list projects, search-by-label)
│   │   │   └── oauthtoken/             #   ReactiveTokenManager + AES-256-GCM TokenCipher (PROVIDER tokens)
│   │   ├── ticketreport/              # NHI business: create ticket, recent-tickets cache, drift reconcile
│   │   ├── automation/                # blog-digest: RunOnce pipeline + CRUD use-cases + scheduler loop
│   │   │   ├── discover/              #   sitemap.xml (+ index) parser, prefix filter, lastmod ordering
│   │   │   ├── scrape/                #   HTTP fetch → go-readability → markdown
│   │   │   └── summarize/            #   thin Ollama client (POST /api/generate)
│   │   ├── transport/http/            # gin router, middleware, handlers, DTOs, identity resolvers
│   │   ├── config/                    # viper load + fail-fast validation; centralized keys
│   │   ├── logging/                   # slog setup + request-id propagation
│   │   ├── httpconst/, secret/        # shared HTTP header consts; constant-time secret helpers
│   │   ├── platform/                  # pgxpool, redis client, http server, graceful shutdown, pprof
│   │   └── storage/
│   │       ├── postgres/              # tenant/user/apitoken/credential/automation repos (set per-tx tenant GUC)
│   │       └── redis/                 # session, token cache, rate limit, oauth state, ticket cache, reconcile gate, automation seen-set
│   ├── migrations/                    # golang-migrate SQL: 000001_init (tables+RLS+role+funcs), 000002_automations
│   ├── docs/                          # generated OpenAPI spec (swaggo): docs.go, swagger.{json,yaml}
│   ├── test/integration/             # testcontainers end-to-end (real PG + Redis, Jira + Ollama mocked)
│   └── go.mod
├── frontend/                          # React + Vite + TS SPA
│   ├── src/
│   │   ├── App.tsx, main.tsx          # shell + topbar (Sign out, /api_docs ↗)
│   │   ├── api.ts, types.ts           # fetch wrapper (CSRF header, error envelope) + DTO types
│   │   ├── styles.css                 # dark theme via CSS vars
│   │   └── components/                # Login, Dashboard, TicketsPanel, TokensPanel, AutomationPanel
│   ├── nginx.conf                     # serves SPA + proxies /v1|/healthz|/readyz|/api_docs → backend
│   └── Dockerfile
├── deployments/docker/
│   ├── Dockerfile                     # backend (used by api/seed/scheduler): multi-stage, distroless, non-root
│   ├── ollama.Dockerfile              # FROM ollama/ollama; pre-pulls the model at build (starts warm)
│   └── postgres-initdb.sh             # grants LOGIN+password to the least-privilege app role
├── docs/superpowers/                 # design specs + implementation plans (drift, automation)
├── docker-compose.yml                 # frontend, backend, scheduler, ollama, postgres, redis, migrate, seed
├── Taskfile.yml                       # go-task: the single local entrypoint
├── .env.example                       # documented configuration (copy to .env)
└── README.md
```

---

## Data model

> **Full column / index / TTL reference — every Postgres table, the RLS
> mechanism, and all Redis keys — is in [docs/DATA-MODEL.md](docs/DATA-MODEL.md).**

Single Postgres database. **Sessions, caches, and rate-limit counters live in
Redis, not Postgres.** There is **no tickets table** — the recent-tickets view is
a Redis cache rebuilt from Jira (the source of truth).

```text
  tenants ──1:N──┬── users ──1:N──┬── api_tokens               (SHA-256 hash, scopes, prefix)
                 │                 ├── integration_credentials  (AES-GCM access+refresh, cloudid, status)
                 │                 └── automations (owner)      (watched site, schedule, run state)
                 │
  enum connection_status = { connected, needs_reauth, revoked }
  enum automation_status = { idle, running }
```

| Table | Purpose | RLS | Notable indexes |
|---|---|---|---|
| `tenants` | organizations (reference data, read pre-tenant by slug) | — | `UNIQUE(slug)` |
| `users` | login identities; `email` globally unique | ✓ | `UNIQUE(email)`, `idx_users_tenant_fk` |
| `api_tokens` | our machine keys | ✓ | `UNIQUE(token_hash)`, `(tenant_id, owner_id, created_at DESC)`, owner FK |
| `integration_credentials` | encrypted Jira tokens, one per `(tenant,user,provider)` | ✓ | `UNIQUE(tenant_id, user_id, provider)`, user FK |
| `automations` | watched site + schedule + run state (tenant-shared, owner's credential) | ✓ | partial `(next_scan_at) WHERE enabled`, tenant + owner FK |

**Row-Level Security (defense layer 3).** Tenant tables `ENABLE ROW LEVEL
SECURITY` with `USING (tenant_id = app_current_tenant())`, where
`app_current_tenant()` reads the per-transaction `app.tenant_id` GUC the
repositories set from the request `Identity`. When the GUC is unset the policy
matches no rows (deny-by-default). The app connects as the **non-superuser**
`identityhub_app` role so RLS actually applies; migrations/admin run as the
superuser (which bypasses RLS by design — tests rely on this). Three pre-tenant
bootstrap operations go through narrow `SECURITY DEFINER` functions instead of
weakening the policies: login-by-email (`find_user_for_login`), API-key-by-hash
(`find_api_token_by_hash`), and the scheduler's cross-tenant due-row claim
(`claim_due_automations`, which atomically takes due rows `FOR UPDATE SKIP
LOCKED` and marks them running).

---

## Key design decisions (and why)

Opaque server-side sessions over JWT, reactive (lazy) token refresh,
defense-in-depth multi-tenancy with Postgres RLS, careful secret handling
(Argon2id / AES-256-GCM / SHA-256), and treating Jira as the source of truth for
tickets.

> **Each decision, with its full rationale and trade-offs, is written up in
> [docs/DECISIONS.md](docs/DECISIONS.md).**

---

## REST API (for scanners / CI)

Interactive docs (Swagger UI) live at **http://localhost:3000/api_docs/index.html** —
the OpenAPI spec is generated from handler annotations (`task swag` to regenerate).
Browser/session-only flows (login/logout, OAuth connect/callback) are intentionally
excluded from the spec, which documents the **machine-consumable** API.

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
| `GET /v1/tokens` | session \| key | list keys (metadata only) |
| `DELETE /v1/tokens/{id}` | session \| key | revoke key |
| `GET /v1/integrations` | session \| key | list integrations (connection status) |
| `GET /v1/integrations/jira/connect` | session | start OAuth (browser flow) |
| `GET /v1/integrations/jira/callback` | session | finish OAuth (browser redirect) |
| `GET /v1/integrations/jira/status` | session \| key | connection status |
| `GET /v1/integrations/jira/projects` | session \| key | list projects (picker) |
| `POST /v1/integrations/jira/tickets` | session \| key:`integrations:write` | create finding (tagged `identityhub`) |
| `GET /v1/integrations/jira/tickets?project=KEY` | session \| key | recent tickets (cached) |
| `POST /v1/integrations/jira/reconcile` | session \| key | refresh the cache from Jira (drift) |
| `DELETE /v1/integrations/jira` | session \| key:`integrations:write` | disconnect |
| `GET /v1/automations` | session \| key | list automations (tenant-wide) |
| `POST /v1/automations` | session | create an automation |
| `GET /v1/automations/{id}` | session \| key | get one |
| `PUT /v1/automations/{id}` | session | update (full replacement) |
| `DELETE /v1/automations/{id}` | session | delete (also clears the seen-set) |
| `POST /v1/automations/{id}/run` | session | run now (`next_scan_at = now()`) |
| `GET /healthz`, `/readyz` | public | liveness / readiness |

Errors use a uniform envelope `{"error":"code","message":"..."}`; a
`409 reauth_required` signals the integration must be reconnected (call
`/connect` again).

---

## Configuration reference

All config is loaded by viper: defaults → optional config file (`CONFIG_FILE`) →
env vars (env wins). Nested keys map to `UPPER_SNAKE` (e.g. `postgres.host` →
`POSTGRES_HOST`). Copy `.env.example` to `.env` and fill in secrets. Highlights:

| Group | Vars | Notes |
|---|---|---|
| **Runtime** | `ENV`, `LOG_LEVEL` | `dev` (text logs) / `prod` (JSON) |
| **HTTP** | `HTTP_ADDR`, `HTTP_ALLOWED_ORIGINS`, `PPROF_ENABLED`, `PPROF_ADDR` | pprof on its own internal port |
| **Postgres (admin)** | `PG_SUPERUSER`, `PG_SUPERUSER_PASSWORD`, `PG_DB`, `APP_DB_PASSWORD` | superuser runs migrations; app role gets `APP_DB_PASSWORD` |
| **Postgres (app conn)** | `POSTGRES_HOST/PORT/USER/PASSWORD/DB`, `POSTGRES_SSLMODE`, `POSTGRES_MAX_CONNS` | `USER` = least-privilege role; `PASSWORD` must equal `APP_DB_PASSWORD` |
| **Redis** | `REDIS_ADDR`, `REDIS_PASSWORD`, `REDIS_DB` | all shared state |
| **Sessions** | `SESSION_TTL`, `SESSION_ABSOLUTE_TTL`, `SESSION_COOKIE_NAME`, `SESSION_COOKIE_SECURE` | set `SECURE=true` behind TLS |
| **Crypto** | `CRYPTO_TOKEN_KEY` | base64 32-byte key — `openssl rand -base64 32` |
| **API keys** | `API_TOKEN_PREFIX`, `API_TOKEN_CACHE_TTL` | plaintext prefix + Redis cache TTL |
| **Rate limit** | `RATELIMIT_LOGIN_MAX`, `RATELIMIT_LOGIN_WINDOW` | per-IP and per-account on login |
| **Jira 3LO** | `JIRA_CLIENT_ID/SECRET`, `JIRA_REDIRECT_URI`, `JIRA_SCOPES`, `JIRA_AUTH_URL`, `JIRA_TOKEN_URL`, `JIRA_API_BASE_URL`, `JIRA_USE_PKCE`, `JIRA_INACTIVITY_WINDOW`, `JIRA_ACCESS_TOKEN_SKEW` | redirect URI is derived from `PUBLIC_HOST` unless set |
| **Ollama (LLM)** | `OLLAMA_BASE_URL`, `OLLAMA_MODEL`, `OLLAMA_TIMEOUT`, `OLLAMA_MAX_INPUT_CHARS`, `OLLAMA_MEM_LIMIT` | model is pre-pulled at image build; memory-capped |
| **Scheduler** | `SCHEDULER_TICK`, `SCHEDULER_CLAIM_BATCH`, `SCHEDULER_LEASE`, `SCHEDULER_MEM_LIMIT` | poll interval, claim batch size, crash-lease |
| **Automation** | `AUTOMATION_MAX_POSTS_PER_RUN`, `AUTOMATION_DEFAULT_INTERVAL`, `AUTOMATION_HTTP_TIMEOUT` | per-run cap drains backlogs over runs |
| **Seed** | `SEED_ORG_SLUG/NAME`, `SEED_USER_EMAIL/PASSWORD` | the first org + login |
| **Public origin** | `PUBLIC_HOST`, `FRONTEND_PORT` | callback derives from these |

The mandatory secrets are **fail-fast**: `docker compose up` aborts before any
container starts if `POSTGRES_USER/PASSWORD/DB`, `REDIS_PASSWORD`,
`CRYPTO_TOKEN_KEY`, or the `PG_*`/`APP_DB_PASSWORD` admin vars are unset.

---

## Adding another provider (3 steps)

The core (`integration`, `ticketreport`, `transport`) depends only on small
consumer-defined ports (the OAuth provider and the operations `Client`). To add
e.g. GitHub:

1. Implement `XxxOAuthProvider` in `integration/oauth` and `XxxClient` in
   `integration/client`.
2. Add its config block.
3. Register it in the composition root (`internal/app`).

No changes to the orchestration, token manager, or transport. (This PoC ships
Jira only by choice; the seams are there.)

---

## Testing

```bash
task test               # unit tests, table-driven + AAA, race detector
task test-integration   # end-to-end against real Postgres + Redis (testcontainers), Jira mocked
task cover              # combined statement coverage
```

Integration tests cover: login→session→logout; API-key issue→use→revoke; the full
Jira flow (connect→callback with state+identity cross-check→**encrypted**
credential→create ticket→recent tickets→reconcile→disconnect); the reauth/reconnect
flow; multi-tenant isolation proven at the repository **and** RLS layers; and the
**automation** pipeline (`claim_due_automations` with `SKIP LOCKED` + lease reaper,
create→run against an httptest blog + mock Ollama→ticket created→appears in recent
tickets→second run creates no duplicate).

---

## Assumptions & scope

- **Login is by email + password**; email is globally unique and the tenant is
  derived from the matched user. The pre-tenant lookup uses a narrow
  `SECURITY DEFINER` function (`find_user_for_login`) — the same pattern as the
  API-key-by-hash lookup — so RLS stays enforced for everything else. Demo org
  `acme` + user are seeded.
- **"Recent tickets" + drift reconciliation.** Every ticket IdentityHub creates is
  tagged `identityhub`. **Jira is the source of truth**: the recent-tickets view
  is a per-tenant **Redis cache** (TTL) of the Jira label search, so it discovers
  tickets created by *any* tenant user (and pre-existing ones on a fresh start).
  The cache is reconciled usage-driven — async on connect and via an explicit
  refresh (`POST …/reconcile`) — throttled + single-flighted by a Redis gate so a
  burst of connects collapses to one reconcile. (No Postgres tickets table; the
  cache self-heals from Jira.)
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
