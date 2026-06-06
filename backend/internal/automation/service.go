// Package automation is the blog-digest bounded context: it manages automations
// (CRUD) and runs a single automation's pipeline (discover -> diff unseen ->
// scrape -> summarize -> create ticket). It defines small consumer ports; the
// concrete adapters are wired in internal/app.
package automation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/logging"
)

// maxTitleLen mirrors the HTTP ticket title bound (Jira summary limit).
const maxTitleLen = 255

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
	Disc            Discoverer
	Scraper         Scraper
	Summ            Summarizer
	Tickets         TicketCreator
	Seen            SeenSet
	MaxPostsPerRun  int
	DefaultInterval time.Duration
	Now             func() time.Time
}

// Service implements automation CRUD and the per-run pipeline.
type Service struct {
	repo            Repository
	disc            Discoverer
	scraper         Scraper
	summ            Summarizer
	tickets         TicketCreator
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
		repo: d.Repo, disc: d.Disc, scraper: d.Scraper, summ: d.Summ,
		tickets: d.Tickets, seen: d.Seen, maxPostsPerRun: d.MaxPostsPerRun,
		defaultInterval: d.DefaultInterval, now: now,
	}
}

// RunOnce executes one full pipeline for an automation. It returns
// ErrReauthRequired if the owner's Jira connection needs reconnecting (the run
// aborts, leaving remaining posts unseen for the next run). Per-post scrape /
// summarize / create failures are logged and skipped (left unseen to retry).
func (s *Service) RunOnce(ctx context.Context, a domain.Automation) error {
	log := logging.FromContext(ctx)

	urls, err := s.disc.Discover(ctx, a.SiteURL)
	if err != nil {
		return fmt.Errorf("discover: %w", err)
	}
	unseen, err := s.seen.Unseen(ctx, a.ID, urls)
	if err != nil {
		return fmt.Errorf("filter unseen: %w", err)
	}
	if s.maxPostsPerRun > 0 && len(unseen) > s.maxPostsPerRun {
		unseen = unseen[:s.maxPostsPerRun]
	}

	principal := domain.Identity{TenantID: a.TenantID, UserID: a.OwnerUserID}
	for _, postURL := range unseen {
		title, markdown, err := s.scraper.Scrape(ctx, postURL)
		if err != nil {
			log.Warn("scrape post failed", logging.Err(err))
			continue
		}
		summary, err := s.summ.Summarize(ctx, title, markdown)
		if err != nil {
			log.Warn("summarize post failed", logging.Err(err))
			continue
		}
		_, err = s.tickets.CreateTicket(ctx, principal, domain.TicketPayload{
			ProjectKey:  a.ProjectKey,
			Title:       composeTitle(summary, title),
			Description: composeDescription(summary, postURL),
			Labels:      []string{"identityhub", "blog-digest"},
		})
		if err != nil {
			if errors.Is(err, domain.ErrReauthRequired) {
				return domain.ErrReauthRequired // no point continuing this run
			}
			log.Warn("create ticket failed", logging.Err(err))
			continue
		}
		if err := s.seen.Add(ctx, a.ID, postURL); err != nil {
			log.Warn("mark seen failed", logging.Err(err))
		}
	}
	return nil
}

// composeTitle builds the ticket summary as "<source> (<type>) <title>", dropping
// any part the summarizer left empty and falling back to the scraped page title.
func composeTitle(sum domain.PostSummary, fallbackTitle string) string {
	title := strings.TrimSpace(sum.Title)
	if title == "" {
		title = strings.TrimSpace(fallbackTitle)
	}
	var b strings.Builder
	if source := strings.TrimSpace(sum.Source); source != "" {
		b.WriteString(source)
		b.WriteString(" ")
	}
	if typ := strings.TrimSpace(sum.Type); typ != "" {
		b.WriteString("(")
		b.WriteString(typ)
		b.WriteString(") ")
	}
	b.WriteString(title)
	return truncate(strings.TrimSpace(b.String()), maxTitleLen)
}

// composeDescription builds a stable, parseable ticket body: the prose summary,
// then a delimiter and labelled Source/Link lines (the origin link matters most).
func composeDescription(sum domain.PostSummary, postURL string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(sum.Body))
	b.WriteString("\n\n---\n")
	if source := strings.TrimSpace(sum.Source); source != "" {
		b.WriteString("Source: ")
		b.WriteString(source)
		if typ := strings.TrimSpace(sum.Type); typ != "" {
			b.WriteString(" (")
			b.WriteString(typ)
			b.WriteString(")")
		}
		b.WriteString("\n")
	}
	b.WriteString("Link: ")
	b.WriteString(postURL)
	return b.String()
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
