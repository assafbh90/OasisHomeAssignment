# IdentityHub — Engineering Conventions

Read this before changing code. It captures how this repo is built so contributions
(human or AI) stay consistent. These rules describe the *existing* patterns — match
them; don't invent new ones without a reason.

## Architecture

- **Modular monolith, hexagonal / ports-and-adapters.** One Go backend (`backend/`)
  plus a decoupled React SPA (`frontend/`). Each `internal/` package is a bounded
  context that could be extracted into its own service.
- **Consumer-defined interfaces.** A package declares the *small* interface it needs,
  next to where it's used (e.g. `reportService` in the HTTP handler, `Summarizer` in
  `automation`). Adapters that satisfy them live in `storage/{postgres,redis}`,
  `integration/{oauth,client}`, `automation/{discover,scrape,summarize}`.
- **Composition root only in `internal/app`.** Concrete types are bound to interfaces
  in `app.Wire`; nowhere else. `cmd/api` is a thin shell that calls `app.Run`
  (which dispatches `serve` | `seed` | `scheduler`).
- **`internal/domain` is pure.** Value types, sentinel errors, and constants only —
  no infra imports (no gin, pgx, redis, http).

## Naming & constants  (← common review miss)

- **No magic strings/numbers for shared values.** Centralize them:
  - Provider/label/scope values live in `domain` (`ProviderJira`, `IdentityHubLabel`,
    `BlogDigestLabel`, `ScopeIntegrationsWrite`, …). Reference the const — never
    re-type the literal.
  - Config keys live once in `config/keys.go` (`keyXxx = "group.field"`); env-var form
    is the `UPPER_SNAKE` of the key. Add a key there, then read it in `config.Load`
    and give it a default in `setDefaults` — keep all three in lockstep.
  - Per-package tunables (timeouts, limits, TTLs, paths) are named `const`s at the top
    of the file with a comment explaining the *why*, not inline literals.
- **Sentinel errors** are `domain.ErrXxx`; reference and wrap those, don't make new
  ad-hoc error strings for the same condition.
- **Adapters are named `<Tech><Role>`**: `PostgresUserRepository`, `RedisTicketCache`,
  `JiraClient`, `ReactiveTokenManager`, `AESGCMTokenCipher`.
- **Exported identifiers have a doc comment** starting with the identifier name.

## Go style

- `context.Context` is the first parameter of any I/O or request-scoped function.
- Wrap errors with `%w` and context (`fmt.Errorf("discover: %w", err)`); map to HTTP
  status only at the transport boundary (`respondError` / the uniform error envelope).
- Prefer the **`samber/lo`** helpers (`lo.Map`, `lo.Filter`, `lo.FilterMap`) for
  pure transforms — *but* keep a plain `for` loop when the body has early returns,
  error handling, side effects, or an awkward element type. Readability wins over
  dogma.
- Logging is `slog` via `internal/logging` (`logging.FromContext`, `logging.Err`).
  **Never log secrets** — only IDs, prefixes, and tenant/user IDs.
- Keep files focused; when one grows past a single clear responsibility, split it.

## Multi-tenancy & security (non-negotiable)

- Handlers read tenant/user **only** from the `domain.Identity` on the request
  context — never from request body/query/path.
- Every tenant-scoped query filters by `tenant_id` **and** the repo sets the per-tx
  `app.tenant_id` GUC (RLS is the third layer). The app connects as the
  non-superuser role; migrations/admin use the superuser.
- Pre-tenant reads (login-by-email, API-key-by-hash, scheduler claim) go through
  narrow `SECURITY DEFINER` functions, not by weakening RLS policies.
- Secrets: Argon2id passwords, AES-256-GCM provider tokens, SHA-256 API-key hashes,
  constant-time comparison. `.env` is git-ignored and must never be committed.

## HTTP / API

- gin lives only in `transport/http`. Request/response DTOs live in `dto.go`;
  validate in a `validate()` method returning a message string.
- Document **consumable** endpoints with swaggo annotations (`@Summary/@Tags/...`);
  browser-only flows (login/logout, OAuth connect/callback) stay out of the spec.
  Regenerate with `task swag` after changing annotations.

## Testing

- Table-driven, AAA, `testify/require`. Unit tests use fakes built from the
  consumer interfaces; adapter tests use `httptest`; cross-cutting tests use
  testcontainers under the `integration` build tag.
- Run with `-race`. Mock external services (Jira, Ollama) via base-URL override.

## Tooling (the verify loop)

```bash
task fmt              # gofmt + go vet
task test             # unit, -race -short
task test-integration # testcontainers (needs Docker)
task swag             # regenerate OpenAPI from annotations
```

> `golangci-lint` currently panics on this module's Go toolchain, so the source of
> truth for "clean" is **gofmt + go vet + the test suites** — always run those before
> committing.

## Frontend

- React + Vite + TS. Named-function components; data access through the `api` wrapper
  (`src/api.ts`), which attaches the CSRF header and normalizes the error envelope.
- Shared types in `src/types.ts`; dark theme via CSS variables in `styles.css`
  (reuse existing classes — `card`, `tab`, `badge`, `alert`, `chip` — before adding new ones).

## Git

- Commit only when asked. Conventional-commit subjects (`feat(scope):`, `docs:`,
  `fix:`). Never stage `.env`, `node_modules`, `dist`, or build artifacts.
