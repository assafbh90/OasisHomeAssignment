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

### Names describe role and operation

Names must explain themselves. A reader should know what a thing *is* or *does*
from its name alone — not from its type or surrounding code.

- **Types/structs are named for what they are/do**, not generic nouns. Good:
  `ReactiveTokenManager`, `RedisReconcileGate`. Avoid vague names like `Manager`,
  `Helper`, `Handler` (without a qualifier), or `Gate` standing alone.
- **Methods are verbs naming the operation**: `TryAcquire`, `EnsureConnected`,
  `ListRecentTickets`, `FetchValidToken`. Avoid vague verbs like `Begin`, `Do`,
  `Process`, `Handle`, or `Wire` — say what actually happens (`Build`, `Reconcile`).
- **Struct fields say what they hold**: `reconcileGate`, `tokenManager`,
  `providerClient` — not `gate`, `tokens`, `client` when the role is ambiguous.
- **Encode units in the name** of any numeric quantity whose unit isn't obvious
  from its type: `maxInputChars`, `maxSitemapBytes`, `intervalSeconds`,
  `durationMs`. (A `time.Duration` already carries its unit — don't suffix those.)
- **Variables earn descriptive names**; single letters are reserved for the two
  idiomatic Go exceptions: **method receivers** (`func (s *Service)`) and **tight
  loop indices** (`for i := range`). Everything else spells it out.
- **Extract complex/multi-part conditions into a named predicate** that reads as a
  statement of fact — the name says what the condition *means*, never how it's
  computed. `internal/domain` is the reference standard (`HasScope`, `NeedsReauth`,
  `IsActive`, `IsExpired`, `BelongsToTenant`, `IsSamePrincipal`); match that style
  everywhere. In-tree examples beyond domain: `httpconst.IsSuccessStatus(code)` (vs
  `code < 200 || code >= 300`), `searchResponse.isFinalPage()`, `isInvalidGrantError(err)`.
  A repeated multi-part condition is also a DRY signal — extract it to one shared,
  named predicate. Single, already-obvious comparisons stay inline.
- **Filename reflects the file's primary type/responsibility.** When you rename the
  main type or change what a file is about, rename the file to match with `git mv`
  (preserve history): `ReactiveTokenManager` in `reactive_token_manager.go`; if a
  type were renamed the file follows. Filenames are lowercase `snake_case` for
  multi-word names (e.g. `auth_handler.go`, `reactive_token_manager.go`); avoid only
  the suffixes Go reads as build constraints (`_test`, `_<GOOS>`, `_<GOARCH>`). Keep
  each `_test.go` named for the file it tests.

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

## Conventions

Two rules that override personal preference. Both are about clarity, not ceremony.

### 1. Use `samber/lo` where it best fits — don't force it

- Prefer the `lo` helper when it makes intent clearer than a hand-rolled loop:
  `lo.Map`, `lo.Filter`, `lo.FilterMap`, `lo.Reduce`, `lo.GroupBy`, `lo.Uniq`,
  `lo.Contains`, `lo.Find`, `lo.Keys`/`lo.Values`, `lo.Associate`/`lo.SliceToMap`,
  `lo.Chunk`, `lo.FlatMap`. A named transform reads as intent ("keep the unseen
  ones") where a loop reads as mechanism.
- "Best fit" is a readability judgment, **not a mandate**. Keep a plain `for` loop
  when the body has **side effects, early returns, error handling, or shared-state
  mutation**; on a **hot path** where the extra closure/allocation matters; over an
  **awkward element type** (e.g. an anonymous struct) where `lo` adds noise; or when
  it's a single trivial loop `lo` would only obscure. When in doubt, leave the loop.
- Examples in-tree: `lo.Map`/`lo.Filter` in `ticketreport`, `ticketcache`, the
  handlers, `session`; `lo.FilterMap` in `jira` label sanitizing. Left as loops on
  purpose: DB `rows.Next()` scans (error handling), Jira project mapping (anonymous
  struct), sitemap discovery (branch + continue-on-error), the seen-set map build
  (O(1) lookups beat `lo.Contains`).

### 2. Test our own logic — no dependency tests, no duplicates

- Test **our** functions, business rules, branches, and edge cases.
- **Never test third-party or stdlib behavior.** We trust `samber/lo`, the standard
  library, `argon2id`, etc. A test whose only real assertion is that a library does
  what it documents (`lo.Map` transforms each element, `json.Marshal` makes JSON,
  argon2 uses a random salt) is testing someone else's code — delete it.
- When **our** logic is the function passed into a `lo` helper (a transform/
  predicate), test that function directly with our inputs/outputs — not through the
  library call.
- **No duplicate coverage.** Each meaningful behavior is covered once; consolidate
  overlapping or near-identical cases into one table-driven test and delete the rest.
- Match the repo style: standard `testing`, table-driven, AAA, `testify/require`;
  fakes from the consumer interfaces; `httptest` for adapters; testcontainers under
  the `integration` tag. Always run `go test ./...` before finishing.

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
