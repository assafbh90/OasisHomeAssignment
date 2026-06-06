# IdentityHub — Architecture

> Extracted from the README so the top-level doc stays lean. Covers the
> system view, the hexagonal package layout, the two token worlds, scalability,
> and the per-request lifecycles.

### System view

The default deployment is long-lived containers (frontend, backend, scheduler,
ollama, postgres, redis) plus one-shot `migrate` and `seed`. The SPA's nginx
serves static assets **and** reverse-proxies the API, so the browser stays
same-origin and `HttpOnly` session cookies work without CORS.

```text
   browser (SPA, cookie)  ┐
                          ├─► ┌───────────────┐  /v1/*  ┌───────────────────┐
   scanner / CI (API key) ┘   │   frontend    │  proxy  │     backend       │
                              │   nginx :80    ├────────►│   gin :8080       │
                              │ • serves SPA   │         │  (N stateless     │
                              │ • /api_docs    │         │   replicas)       │
                              └───────────────┘         └───┬───────────┬───┘
                                                            │           │
                   ┌────────────────────┐                  │           │
                   │  scheduler worker  │── claim due ──────┤           │
                   │ (automation runs)  │   (Postgres)      │           │
                   └─────────┬──────────┘                   │           │
                             │ summarize                    │           │
                             ▼                              │           │
                   ┌────────────────────┐                  │           │
                   │   ollama (local    │       ┌──────────┘           │
                   │   LLM, mem-limited)│       ▼                      ▼
                   └────────────────────┘  ┌────────────────────┐  ┌────────────────────┐
                                           │     Postgres 16    │  │      Redis 7       │
                                           │ users · api_tokens │  │ sessions · token   │
                                           │ credentials (enc.) │  │ cache · rate limit │
                                           │ automations        │  │ oauth state ·      │
                                           │ + RLS per tenant   │  │ ticket cache +     │
                                           └────────────────────┘  │ reconcile gate ·   │
                                                                   │ automation seen-set│
                   pprof :6060 (internal only)                     └────────────────────┘

   backend / scheduler ──── OAuth 3LO + REST ────► Jira Cloud (user's own workspace)
```

### Two subsystems behind one API, joined by `Identity`

The auth middleware resolves the caller from a **session cookie** or a **Bearer
API key**, builds an `Identity{UserID, TenantID, Scopes, AuthMethod}`, and injects
it into the request context. **Every protected handler — including all
integration endpoints — reads tenant/user from `Identity`, never from request
input.** That value object is the seam between the auth and integration code.

```text
                                        ┌─ auth          sessions · users · API keys
  browser ─┐                            │
           ├─ SPA (nginx, proxies /v1) ─┤  integration   OAuth · encrypted tokens · reactive refresh
  scanner ─┘   └─ REST ── api ──Identity┼─ ticketreport  create ticket · recent-tickets view (Redis) · drift reconcile
                                        │
                                        └─ automation    watch site → discover → summarize → file ticket (scheduler)
```

### Hexagonal, dependency-inverted

Interfaces are **consumer-defined** (each feature package declares the small
interface it needs); concrete adapters live in `storage/postgres`,
`storage/redis`, `integration/oauth`, `integration/client`. Wiring happens
**only** in the composition root (`internal/app`, called from `cmd/api`). The
domain is pure Go with no infra imports.

```text
   transport/http (gin router/handlers)     scheduler (automation worker)
              │  depends on small consumer-defined interfaces
              ▼                                        ▼
   ┌──────────┴───────────┬──────────────┬────────────┴─────────┐
   │                      │              │                       │
  auth              integration     ticketreport            automation        ← feature packages
  session           ├ oauth  (3LO)  (NHI business)          ├ discover (sitemap)
  apitoken          ├ client (REST)                         ├ scrape (readability)
                    └ oauthtoken (cipher + refresh)          └ summarize (ollama)
   │                      │              │                       │
   └──────────┬───────────┴──────────────┴───────────────────────┘
              ▼
            domain   (Identity, value objects, sentinel errors — pure)
              ▲
   ┌──────────┴───────────┐
   │  storage/postgres    │   storage/redis        ← adapters implementing the ports
   │  platform (pools,    │   integration/oauth · client
   │  server, shutdown)   │   automation/{discover,scrape,summarize}
   └──────────────────────┘
```

### Two distinct "token worlds" (kept apart by design)

| | What | Where | Package |
|---|---|---|---|
| **API keys (PATs)** | *Our* tokens for scanners/CI | SHA-256 hash in Postgres + Redis cache | `internal/apitoken` |
| **OAuth provider tokens** | *Jira's* access/refresh tokens | AES-256-GCM encrypted in Postgres | `internal/integration/oauthtoken` |

They have separate lifecycles, storage, and revocation — never conflated.

### Scalability

App instances are **stateless**; all shared state (sessions, token cache,
rate-limit counters, OAuth state, the ticket cache + reconcile gate) lives in
**Redis**, so you can run N replicas behind a load balancer
(`docker compose up --scale backend=2`). Validated API keys are Redis-cached to
avoid a DB hit per request. Postgres uses a tuned `pgxpool` (bounded conns,
statement + idle-in-tx timeouts). Graceful shutdown drains in-flight requests for
clean rolling deploys.

---

## Request lifecycles

### 1. Authentication — every request

```text
  request ─► RequestID ─► SecureHeaders ─► [CORS] ─► RequireAuth ─► CSRF ─► RequireScope ─► handler
                                                          │
                                          ┌───────────────┴────────────────┐
                                          │  ChainIdentityResolver          │
                                          │   1. Bearer "ih_pat_…"  → Redis │
                                          │      cache → SHA-256 lookup     │
                                          │   2. session cookie     → Redis │
                                          └───────────────┬────────────────┘
                                                          ▼
                                          Identity{UserID, TenantID, Scopes, AuthMethod}
                                                  put on request context
```

CSRF (double-submit) applies only to **cookie-authenticated** unsafe methods;
Bearer-key calls (scanners/CI) skip it. Some routes additionally require
`RequireSessionMethod` (browser-only: login/logout, OAuth, token issue) or
`RequireScope(integrations:write)`.

### 2. Jira OAuth connect (3LO)

```text
  user clicks "Connect Jira"
        │  GET /v1/integrations/jira/connect
        ▼
  StartAuthorization: make PKCE verifier+challenge, store one-time `state`
        │  bound to {tenant,user} in Redis (TTL 10m) → return Atlassian auth URL
        ▼
  browser → Atlassian consent → redirect to /v1/integrations/jira/callback?state&code
        │
        ▼  GET /v1/integrations/jira/callback   (must carry the session cookie)
  CompleteAuthorization:
        consume state  → cross-check {tenant,user} == session Identity   (anti-mix-up)
        exchange code+verifier → access/refresh tokens (x/oauth2)
        resolve accessible-resource → cloudid + site URL
        AES-256-GCM encrypt tokens → store in integration_credentials (status=connected)
        │
        └─► fire-and-forget reconcileAsync (detached ctx) so a fresh user
            immediately sees pre-existing identityhub-tagged tickets
        ▼
  302 back to the SPA (?connected=jira)
```

### 3. Reactive token refresh

No background warmer — a Jira access token is refreshed **only when expired at
use time**:

```text
  FetchValidToken(tenant,user):
    load credential
      ├─ access token still valid (− skew)?            → return it (no network)
      ├─ status == needs_reauth, or refresh token
      │  provably dead (inactivity window passed)?      → ErrReauthRequired → 409
      └─ otherwise → provider.RefreshTokens
                       ├─ success → persist rotated refresh token + new expiry → return
                       └─ invalid_grant / 4xx → mark needs_reauth → ErrReauthRequired → 409
```

The client surfaces `409 reauth_required` and prompts a reconnect, instead of a
doomed API call or a retry storm. (A cross-replica single-flight on concurrent
refreshes — e.g. `SELECT … FOR UPDATE` on the credential row — is the natural
next step; intentionally left out of this PoC for simplicity.)

### 4. Recent tickets + drift reconciliation

**Jira is the source of truth.** Every ticket IdentityHub creates is tagged with
an `identityhub` Jira label; the "recent tickets" view is a per-tenant **Redis
cache** of the Jira label search — so it surfaces tickets created by *any* tenant
user, and pre-existing ones on a fresh start.

```text
  create ticket  ─► JiraClient.CreateIssue (append `identityhub` label) ─► cache.Add (prepend)

  refresh (button)  ─► POST …/reconcile (force) ─┐
  connect (async)   ─► reconcileAsync ───────────┤
                                                 ▼
                                    RedisReconcileGate.TryAcquire
                              ┌──────────────────┴───────────────────┐
                              │ throttle: skip if reconciled < 30m ago│  (unless forced)
                              │ single-flight: SET NX lock per tenant │  (collapses a
                              └──────────────────┬───────────────────┘   burst of connects)
                                                 ▼
                              JiraClient.SearchByLabel  (JQL labels="identityhub", paginated)
                                                 ▼
                              cache.Replace  (newest N, TTL 24h)
```
