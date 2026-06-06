# NHI Automation (Blog Digest) — Design

_Date: 2026-06-06_

## Goal

Generalize the exercise's optional "NHI Blog Digest" into a reusable **Automation**
subsystem. A tenant member defines an automation that **watches a blog/site URL**
and, on a schedule, discovers new posts, summarizes each with a **local** LLM, and
files a **Jira ticket per post** into a chosen project.

The new code is **only the automation glue**. It reuses the existing Jira client,
reactive token manager, `ticketreport` ticket-creation, Row-Level Security (RLS),
and config/logging patterns unchanged. Self-contained bounded context:
`internal/automation`.

## Key decisions (and why)

- **Separate `scheduler` worker container** (new `cmd/scheduler`), not an in-process
  goroutine. The pipeline is slow and resource-heavy (HTTP fetch + LLM inference);
  isolating it keeps API latency unaffected and lets the scheduler and Ollama be
  **memory-limited independently**. Matches the existing one-shot containers
  (`migrate`, `seed`) and the "each package is extraction-ready" philosophy. Both
  the API and the scheduler share the single composition root (`app.Wire`).
- **Tenant-shared automations, owner's credential.** An automation belongs to a
  tenant and is visible/editable by any tenant member; it pins an `owner_user_id`
  whose stored Jira credential the worker uses to file tickets (via the reactive
  token manager). This matches the current per-user credential model while giving
  team visibility.
- **Discovery via `sitemap.xml` only.** Parse the site's `sitemap.xml` (including a
  sitemap-*index* that points to child sitemaps), filter to URLs **under the watched
  prefix** ("everything under this blog"), newest-first by `lastmod`. No `<a>`-link
  crawling. If no reachable/valid sitemap, the run fails with a clear `last_error`.
- **Minimal self-coded scraper (no Firecrawl).** For each new post: HTTP fetch →
  `go-readability` (strip nav/boilerplate) → HTML-to-markdown. Lightest footprint,
  fully local; accepts weaker handling of JS-rendered sites.
- **Local summarization via a pre-warmed Ollama image.** A custom Docker image
  (`FROM ollama/ollama`) **pre-pulls `qwen2.5:0.5b` at build time** so the container
  starts warm. The model is configurable (build arg + runtime env). The Ollama
  container is **memory-limited** in compose.
- **Redis seen-set per automation** (`automation:seen:{id}`), persisted via the
  existing appendonly Redis. `new = sitemap_urls − SMEMBERS`. A URL is added to the
  set **only after its ticket is created**, so a failed post retries next run rather
  than being silently dropped.
- **Pacing: slow, no overlap, env-configurable.** Posts are processed **one at a
  time** (serializes Ollama → bounds memory). A per-run cap
  (`AUTOMATION_MAX_POSTS_PER_RUN`) drains a backlog over successive runs. A row is
  claimed by flipping `status='idle'→'running'`, so **a new scan never overlaps a
  running one**; `next_scan_at` advances by the per-automation `interval` **only
  after the run completes**. A `locked_at` lease lets a crashed run self-heal.
- **Reuse `ticketreport` for ticket creation.** Automation-created tickets go through
  `ticketreport.Service.CreateTicket`, so they get token refresh + credential load +
  create + are recorded in `created_tickets` — meaning **they also appear in the
  "recent tickets" view** for consistency.

## Architecture & data flow

```
API (backend)                          scheduler (new container, mem-limited)
  CRUD /v1/automations  ──► Postgres ◄── claim due (SKIP LOCKED + lease)
                            automations      │ per automation (tenant-scoped):
                                             ▼
   discover ──► diff vs Redis seen-set ──► scrape ──► summarize (Ollama) ──► create Jira ticket
   sitemap.xml   SMEMBERS seen:{id}        fetch+      qwen2.5:0.5b           reuse ticketreport
   (+ index)     (new = urls − seen)       readability  (mem-limited)         (also shows in
                                          →markdown                            "recent tickets")
                                             └─ on ticket success: SADD seen:{id} url
```

## Data model

New `automations` table (under RLS, keyed by `tenant_id`):

| column | notes |
|---|---|
| `id` | uuid pk |
| `tenant_id` | RLS scope; FK tenants |
| `owner_user_id` | FK users; whose Jira credential is used |
| `name` | display name |
| `site_url` | watched blog/site base (http/https) |
| `provider` | `'jira'` (only provider for now) |
| `project_key` | target Jira project |
| `interval` | scan frequency (duration) |
| `enabled` | bool |
| `status` | `'idle' | 'running'` |
| `next_scan_at` | due time; advances by `interval` on completion |
| `locked_at` | lease stamp for crash self-heal |
| `last_run_at`, `last_error` | surfaced in UI |
| `created_at`, `updated_at` | |

Indexes: a partial/ordered index supporting the due-claim
(`enabled, status, next_scan_at`) plus FK-cascade indexes (`tenant_id`,
`owner_user_id`) consistent with the existing schema's FK-indexing rationale.

**RLS + cross-tenant claim.** The table is under the same `tenant_isolation` policy
as the rest. The scheduler must scan **all** tenants for due rows before any tenant
context exists, so the claim goes through a narrow `SECURITY DEFINER` function
`claim_due_automations(p_limit int, p_lease interval)` — exactly the existing
`find_user_for_login` / `find_api_token_by_hash` pattern. It atomically selects due
rows (`enabled AND (status='idle' OR locked_at < now()-lease) AND next_scan_at<=now()`
`ORDER BY next_scan_at FOR UPDATE SKIP LOCKED LIMIT p_limit`), marks them
`status='running', locked_at=now()`, and returns them. All other reads/writes
(CRUD, completion) stay on the normal RLS path (repo sets `app.tenant_id` from the
known tenant).

**Seen-set.** Redis set `automation:seen:{id}`. Deleting an automation deletes its
set. No TTL (durable record of processed posts), bounded naturally by the site size.

## Packages

New (the only new code):

- `internal/automation`
  - `service.go` — one-run orchestration (`RunOnce(ctx, automation)`) and CRUD
    use-cases. Defines small consumer ports: `Discoverer`, `Scraper`, `Summarizer`,
    `TicketCreator`, `Repo`, `SeenSet`, `Clock`.
  - `scheduler.go` — the loop: tick → `repo.ClaimDue` → `RunOnce` per row →
    `repo.Complete` (advance `next_scan_at`, set `last_run_at`/`last_error`).
- `internal/automation/discover` — `sitemap.xml` + sitemap-index parser; prefix
  filter; `lastmod` newest-first ordering.
- `internal/automation/scrape` — HTTP fetch + `go-readability` + HTML→markdown.
- `internal/automation/summarize` — thin Ollama client
  (`POST {OLLAMA_BASE_URL}/api/generate`, `stream=false`), input truncated to
  `OLLAMA_MAX_INPUT_CHARS`.
- `storage/postgres/automation.go` — `PostgresAutomationRepository` (CRUD +
  `ClaimDue` via the definer function + `Complete`).
- `storage/redis/automationseen.go` — `RedisAutomationSeenSet` (`Diff`, `Add`,
  `Delete`).
- `transport/http/automation_handler.go` (+ DTOs) — CRUD + run-now.
- `cmd/scheduler/main.go` — thin shell calling `app.RunScheduler`.

Reused **unchanged**: `ticketreport.Service.CreateTicket`, `client.JiraClient`,
`oauthtoken` reactive token manager, RLS, `config`, `logging`, composition root.

## Deployment

- `deployments/docker/ollama.Dockerfile`: `FROM ollama/ollama`, `ARG MODEL=qwen2.5:0.5b`,
  pre-pull at build (start `ollama serve` in background → `ollama pull "$MODEL"` →
  stop) so the container starts warm. Changing the model = override the build arg and
  rebuild (a runtime-only change would pull lazily on first use).
- `docker-compose.yml` adds:
  - `ollama` — built from the Dockerfile, with **`mem_limit`** and
    `deploy.resources.limits.memory` (env-tunable via `OLLAMA_MEM_LIMIT`).
  - `scheduler` — reuses the backend image with `command: ["scheduler"]`, also
    memory-limited, `depends_on` postgres/redis/migrate(+ ollama).
- New env (defaults) in `.env.example` and `config`:
  `OLLAMA_BASE_URL`, `OLLAMA_MODEL`, `OLLAMA_TIMEOUT`, `OLLAMA_MAX_INPUT_CHARS`,
  `OLLAMA_MEM_LIMIT`, `SCHEDULER_TICK`, `SCHEDULER_CLAIM_BATCH`, `SCHEDULER_LEASE`,
  `AUTOMATION_MAX_POSTS_PER_RUN`, `AUTOMATION_DEFAULT_INTERVAL`,
  `AUTOMATION_HTTP_TIMEOUT`.
- `Taskfile.yml`: scheduler is covered by `task up`; no new task strictly required.

## API & UI

Endpoints (session-authenticated, UI-driven; uniform error envelope; tenant/user
from `Identity`):

| Method & path | Purpose |
|---|---|
| `GET /v1/automations` | list (tenant-wide) |
| `POST /v1/automations` | create |
| `GET /v1/automations/{id}` | get |
| `PATCH /v1/automations/{id}` | update (name/url/project/interval/enabled) |
| `DELETE /v1/automations/{id}` | delete (also clears the Redis seen-set) |
| `POST /v1/automations/{id}/run` | "run now" (set `next_scan_at = now()`) |

New **Automation** tab (`AutomationPanel.tsx`): list with name, site URL, project,
interval, enabled toggle, **status + last_error** badges, last-run time; create form
(name, site URL, project via the existing select-or-type picker, interval, enabled);
delete; **Run now**. Soft warning at create time if the owner has no connected Jira.

## Error handling & product thinking

`last_error` is surfaced per automation, with clear messages: *"no sitemap.xml found
at <site>"*, *"Jira reconnect required"*, *"Ollama timeout"*. A `reauth_required`
from ticket creation aborts that run cleanly (nothing marked seen), advances
`next_scan_at`, and shows the reconnect hint. Validation: `site_url` must be http(s);
`project_key` required; `interval` has a sane minimum. Per-post failures are logged
and left unseen to retry next run (no poison-pill cap in this PoC).

## Testing

- **Unit (fakes):** only-unseen processed; per-run cap honored; seen updated **only
  on ticket success**; `reauth_required` aborts the run without marking seen;
  `next_scan_at` advances on completion.
- **Adapters (`httptest`):** sitemap + sitemap-index parse, prefix filter, `lastmod`
  ordering; readability→markdown on a fixture; Ollama client request/response shape +
  input truncation.
- **Integration (testcontainers):** `ClaimDue` (`SKIP LOCKED`, lease reaper,
  due/enabled filtering), CRUD, RLS isolation; Redis seen-set round-trip; **e2e**:
  create automation → run → (httptest blog with a sitemap + mock Ollama) → ticket
  created, appears in recent tickets, second run creates no duplicate.

## Out of scope

- Providers other than Jira.
- Link-crawl discovery for sites without a `sitemap.xml`.
- JS-rendered-site scraping (no headless browser).
- Per-post poison-pill retry caps; multiple sites per automation.
- Selecting an owner other than the creator at creation time.
