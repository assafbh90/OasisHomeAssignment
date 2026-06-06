# IdentityHub — Key Design Decisions

> The reasoning and trade-offs behind the major choices. See also
> [ARCHITECTURE.md](ARCHITECTURE.md) and [DATA-MODEL.md](DATA-MODEL.md).

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
  to reconnect — instead of a doomed API call or a retry storm. (Cross-replica
  single-flight via a `SELECT … FOR UPDATE` row lock is the natural next step; it's
  intentionally left out of this PoC for simplicity.)
- **Defense-in-depth multi-tenancy.** (1) Middleware resolves tenant only from the
  session/token. (2) Every repository query filters by `tenant_id` and is passed
  the tenant explicitly. (3) **Postgres Row-Level Security** denies any row whose
  `tenant_id` ≠ the per-transaction `app.tenant_id` GUC. The app connects as a
  **non-superuser** role so RLS actually applies; migrations/admin use the
  superuser. The two pre-tenant lookups (login-by-email, API-key-by-hash) go
  through narrow `SECURITY DEFINER` functions rather than weakening the policy.
- **Secrets handled carefully.** Passwords: Argon2id (~64 MiB, t=3, p=4) via the
  maintained `alexedwards/argon2id`. Jira tokens: AES-256-GCM at rest (random
  nonce per encryption, tamper-detected). API keys: only the SHA-256 hash is
  stored; the plaintext is shown **once**. Constant-time comparisons for secrets.
  OAuth `state` is one-time, bound to `{tenant,user}`, **and** cross-checked
  against the session identity on callback (defends the "callback bound to the
  wrong user" class of attack); PKCE (S256) is used. Rate limiting on login
  (GCRA via `go-redis/redis_rate`). `pprof` runs on a separate internal-only port.
  Secrets never appear in logs (only token IDs/prefixes and tenant/user IDs).
- **Jira is the source of truth for tickets.** Rather than a Postgres mirror that
  can drift, the recent-tickets view is a Redis cache rebuilt from a Jira label
  search — self-healing, and naturally discovers tickets created by other tenant
  users or before the app existed.
