// Package automation is the blog-digest bounded context: it manages automations
// (CRUD) and runs a single automation's pipeline (discover -> diff unseen ->
// scrape -> summarize -> create ticket). It defines small consumer ports; the
// concrete adapters are wired in internal/app.
package automation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/logging"
)

const (
	// maxTitleLen mirrors the HTTP ticket title bound (Jira summary limit).
	maxTitleLen = 255

	// Ticket-body Markdown fragments (rendered to ADF by the Jira client). Kept as
	// named consts so the format is in one place and stays parseable downstream.
	bodyDivider = "\n\n---\n"
	sourceLabel = "**Source:** "
	linkLabel   = "**Link:** "
)

// Discoverer lists candidate post URLs for a site (newest first).
type Discoverer interface {
	Discover(ctx context.Context, siteURL string) ([]string, error)
}

// Scraper fetches a post and returns its title and markdown body.
type Scraper interface {
	Scrape(ctx context.Context, url string) (title string, markdown string, err error)
}

// Summarizer turns a post's title + markdown into a structured summary.
type Summarizer interface {
	Summarize(ctx context.Context, pageTitle, markdown string) (domain.PostSummary, error)
}

// TicketCreator files a ticket. Satisfied by *ticketreport.Service.
type TicketCreator interface {
	CreateTicket(ctx context.Context, principal domain.Identity, payload domain.TicketPayload) (domain.TicketRef, error)
}

// ConnectionChecker reports whether the owner's integration is connected, so the
// runner can stop before scraping/summarizing once it's disconnected. Satisfied
// by *ticketreport.Service. It returns domain.ErrReauthRequired when reconnect is
// needed.
type ConnectionChecker interface {
	EnsureConnected(ctx context.Context, principal domain.Identity) error
}

// SeenSet tracks processed URLs per automation.
type SeenSet interface {
	Unseen(ctx context.Context, automationID uuid.UUID, urls []string) ([]string, error)
	Add(ctx context.Context, automationID uuid.UUID, url string) error
	Clear(ctx context.Context, automationID uuid.UUID) error
}

// Repository persists automations.
type Repository interface {
	Create(ctx context.Context, a *domain.Automation) error
	Get(ctx context.Context, tenantID, id uuid.UUID) (domain.Automation, error)
	ListByTenant(ctx context.Context, tenantID uuid.UUID) ([]domain.Automation, error)
	Update(ctx context.Context, a *domain.Automation) error
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
	SetDue(ctx context.Context, tenantID, id uuid.UUID, at time.Time) error
	ClaimDue(ctx context.Context, batch int, lease time.Duration) ([]domain.Automation, error)
	Complete(ctx context.Context, tenantID, id uuid.UUID, nextScanAt time.Time, runErr string) error
}

// Deps are the collaborators of a Service. Repo may be nil for a runner-only
// Service (the scheduler builds one without CRUD).
type Deps struct {
	Repo            Repository
	Discoverer      Discoverer
	Scraper         Scraper
	Summarizer      Summarizer
	Tickets         TicketCreator
	Connection      ConnectionChecker
	Seen            SeenSet
	MaxPostsPerRun  int
	DefaultInterval time.Duration
	Now             func() time.Time
}

// Service implements automation CRUD and the per-run pipeline.
type Service struct {
	repo            Repository
	discoverer      Discoverer
	scraper         Scraper
	summarizer      Summarizer
	tickets         TicketCreator
	connection      ConnectionChecker
	seen            SeenSet
	maxPostsPerRun  int
	defaultInterval time.Duration
	now             func() time.Time
}

// NewService constructs the service.
func NewService(d Deps) *Service {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		repo: d.Repo, discoverer: d.Discoverer, scraper: d.Scraper, summarizer: d.Summarizer,
		tickets: d.Tickets, connection: d.Connection, seen: d.Seen, maxPostsPerRun: d.MaxPostsPerRun,
		defaultInterval: d.DefaultInterval, now: now,
	}
}

// RunResult summarizes one RunOnce pass so the scheduler can pace the next scan:
// while a backlog remains it reschedules soon (fast drain), otherwise it waits
// the steady-state interval.
type RunResult struct {
	Created int  // tickets created this run
	Backlog bool // more unseen posts remained than this run's cap
}

// RunOnce executes one full pipeline pass for an automation. Posts are processed
// one at a time — scrape -> summarize -> create ticket -> mark seen — so there is
// never more than one Ollama request in flight (synchronous calls are the
// backpressure) and a post is marked seen only after its ticket exists (so a
// failure retries next run). It returns ErrReauthRequired if the owner's Jira
// connection needs reconnecting (the run aborts, leaving the rest unseen).
// Per-post scrape/summarize/create failures are logged and skipped.
func (s *Service) RunOnce(ctx context.Context, a domain.Automation) (RunResult, error) {
	start := s.now()
	// Run-scoped logger: every line for this run carries the automation id + provider.
	log := logging.FromContext(ctx).With(
		slog.String(logging.KeyAutomationID, a.ID.String()),
		slog.String(logging.KeyProvider, a.Provider),
	)

	urls, err := s.discoverer.Discover(ctx, a.SiteURL)
	if err != nil {
		return RunResult{}, fmt.Errorf("discover: %w", err)
	}
	unseen, err := s.seen.Unseen(ctx, a.ID, urls)
	if err != nil {
		return RunResult{}, fmt.Errorf("filter unseen: %w", err)
	}
	// Cap this run and remember whether we left work behind, so the scheduler can
	// keep draining quickly instead of waiting a full interval per batch.
	backlog := false
	if s.maxPostsPerRun > 0 && len(unseen) > s.maxPostsPerRun {
		unseen = unseen[:s.maxPostsPerRun]
		backlog = true
	}
	log.Debug("automation scan",
		slog.Int(logging.KeyDiscovered, len(urls)),
		slog.Int(logging.KeyUnseen, len(unseen)),
		slog.Bool(logging.KeyBacklog, backlog))

	principal := domain.Identity{TenantID: a.TenantID, UserID: a.OwnerUserID}
	created, failed := 0, 0
	// summary emits one analytics line per run (created/failed/backlog/duration).
	summaryLine := func(note string) {
		log.Info("automation run finished",
			slog.String("outcome", note),
			slog.Int(logging.KeyUnseen, len(unseen)),
			slog.Int(logging.KeyCreated, created),
			slog.Int(logging.KeyFailed, failed),
			slog.Bool(logging.KeyBacklog, backlog),
			slog.Int64(logging.KeyDurationMS, s.now().Sub(start).Milliseconds()))
	}

	for _, postURL := range unseen {
		plog := log.With(slog.String(logging.KeyPostURL, postURL))

		// Stop before any scrape/summarize work if the owner's Jira is disconnected
		// (e.g. mid-run). Unprocessed posts stay unseen, so the run resumes from here
		// once reconnected — and we never query Ollama for a post we can't file.
		if s.connection != nil {
			if err := s.connection.EnsureConnected(ctx, principal); err != nil {
				if errors.Is(err, domain.ErrReauthRequired) {
					plog.Info("automation run paused: integration needs reconnect")
					summaryLine("reauth_required")
					return RunResult{Created: created, Backlog: backlog}, domain.ErrReauthRequired
				}
				summaryLine("connection_error")
				return RunResult{Created: created, Backlog: backlog}, fmt.Errorf("check connection: %w", err)
			}
		}

		title, markdown, err := s.scraper.Scrape(ctx, postURL)
		if err != nil {
			failed++
			plog.Warn("scrape post failed", logging.Err(err))
			continue
		}
		summary, err := s.summarizer.Summarize(ctx, title, markdown)
		if err != nil {
			failed++
			plog.Warn("summarize post failed", logging.Err(err))
			continue
		}
		if summary.Source == "" {
			summary.Source = sourceFromURL(postURL) // keep the "[source] ..." title shape
		}
		ref, err := s.tickets.CreateTicket(ctx, principal, domain.TicketPayload{
			ProjectKey:  a.ProjectKey,
			Title:       composeTitle(summary, title),
			Description: composeDescription(summary, postURL),
			Labels:      []string{domain.IdentityHubLabel, domain.BlogDigestLabel},
		})
		if err != nil {
			if errors.Is(err, domain.ErrReauthRequired) {
				plog.Info("automation run paused: integration needs reconnect")
				summaryLine("reauth_required")
				return RunResult{Created: created, Backlog: backlog}, domain.ErrReauthRequired
			}
			failed++
			plog.Warn("create ticket failed", logging.Err(err))
			continue
		}
		created++
		plog.Debug("automation filed ticket", slog.String(logging.KeyIssueKey, ref.IssueKey))
		if err := s.seen.Add(ctx, a.ID, postURL); err != nil {
			plog.Warn("mark seen failed", logging.Err(err))
		}
	}
	summaryLine("ok")
	return RunResult{Created: created, Backlog: backlog}, nil
}

// composeTitle builds the ticket summary as "[<source>] (<type>) <title>",
// dropping any bracketed part the summarizer left empty and falling back to the
// scraped page title.
func composeTitle(sum domain.PostSummary, fallbackTitle string) string {
	title := strings.TrimSpace(sum.Title)
	if title == "" {
		title = strings.TrimSpace(fallbackTitle)
	}
	var b strings.Builder
	if source := strings.TrimSpace(sum.Source); source != "" {
		b.WriteString("[")
		b.WriteString(source)
		b.WriteString("] ")
	}
	if typ := strings.TrimSpace(sum.Type); typ != "" {
		b.WriteString("(")
		b.WriteString(typ)
		b.WriteString(") ")
	}
	b.WriteString(title)
	return truncate(strings.TrimSpace(b.String()), maxTitleLen)
}

// composeDescription builds the ticket body as Jira-friendly Markdown (the client
// renders it to ADF): the summary, a divider, then bold-labelled Source/Link
// lines (the origin link matters most). The labels are also stable enough to parse.
func composeDescription(sum domain.PostSummary, postURL string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(sum.Body))
	b.WriteString(bodyDivider)
	if source := strings.TrimSpace(sum.Source); source != "" {
		b.WriteString(sourceLabel)
		b.WriteString(source)
		if typ := strings.TrimSpace(sum.Type); typ != "" {
			b.WriteString(" (")
			b.WriteString(typ)
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
	b.WriteString(linkLabel)
	b.WriteString(postURL)
	return b.String()
}

// sourceFromURL derives a fallback source name from a URL host (stripping a
// leading "www.") so the ticket title keeps its "[source] (type) title" shape when
// the summarizer didn't supply a source.
func sourceFromURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.TrimPrefix(u.Host, "www.")
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// CreateInput is the data for a new automation (owner is the caller).
type CreateInput struct {
	Name       string
	SiteURL    string
	ProjectKey string
	Interval   time.Duration
	Enabled    bool
}

// UpdateInput holds optional field changes (nil = leave unchanged).
type UpdateInput struct {
	Name       *string
	SiteURL    *string
	ProjectKey *string
	Interval   *time.Duration
	Enabled    *bool
}

// Create makes a new automation owned by the caller, due immediately so the
// first scan runs on the next tick.
func (s *Service) Create(ctx context.Context, principal domain.Identity, in CreateInput) (domain.Automation, error) {
	interval := in.Interval
	if interval <= 0 {
		interval = s.defaultInterval
	}
	a := domain.Automation{
		TenantID:    principal.TenantID,
		OwnerUserID: principal.UserID,
		Name:        in.Name,
		SiteURL:     in.SiteURL,
		Provider:    domain.ProviderJira,
		ProjectKey:  in.ProjectKey,
		Interval:    interval,
		Enabled:     in.Enabled,
		NextScanAt:  s.now(),
	}
	if err := s.repo.Create(ctx, &a); err != nil {
		return domain.Automation{}, err
	}
	return a, nil
}

// List returns the tenant's automations.
func (s *Service) List(ctx context.Context, principal domain.Identity) ([]domain.Automation, error) {
	return s.repo.ListByTenant(ctx, principal.TenantID)
}

// Get returns one automation.
func (s *Service) Get(ctx context.Context, principal domain.Identity, id uuid.UUID) (domain.Automation, error) {
	return s.repo.Get(ctx, principal.TenantID, id)
}

// Update applies the supplied field changes.
func (s *Service) Update(ctx context.Context, principal domain.Identity, id uuid.UUID, in UpdateInput) (domain.Automation, error) {
	a, err := s.repo.Get(ctx, principal.TenantID, id)
	if err != nil {
		return domain.Automation{}, err
	}
	if in.Name != nil {
		a.Name = *in.Name
	}
	if in.SiteURL != nil {
		a.SiteURL = *in.SiteURL
	}
	if in.ProjectKey != nil {
		a.ProjectKey = *in.ProjectKey
	}
	if in.Interval != nil {
		a.Interval = *in.Interval
	}
	if in.Enabled != nil {
		a.Enabled = *in.Enabled
	}
	if err := s.repo.Update(ctx, &a); err != nil {
		return domain.Automation{}, err
	}
	return a, nil
}

// Delete removes an automation and clears its seen-set.
func (s *Service) Delete(ctx context.Context, principal domain.Identity, id uuid.UUID) error {
	if err := s.repo.Delete(ctx, principal.TenantID, id); err != nil {
		return err
	}
	return s.seen.Clear(ctx, id)
}

// RunNow makes an automation due immediately.
func (s *Service) RunNow(ctx context.Context, principal domain.Identity, id uuid.UUID) error {
	return s.repo.SetDue(ctx, principal.TenantID, id, s.now())
}
