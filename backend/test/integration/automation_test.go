//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/automation"
	"github.com/assafbh/identityhub/internal/automation/discover"
	"github.com/assafbh/identityhub/internal/automation/scrape"
	"github.com/assafbh/identityhub/internal/automation/summarize"
	"github.com/assafbh/identityhub/internal/domain"
	store "github.com/assafbh/identityhub/internal/storage/postgres"
	redisstore "github.com/assafbh/identityhub/internal/storage/redis"
)

// seedAutomationOwner inserts a tenant + user directly (admin pool, bypasses RLS) and
// returns their IDs for use as an automation owner.
func seedAutomationOwner(t *testing.T, ctx context.Context) (uuid.UUID, uuid.UUID) {
	t.Helper()
	var tenantID, userID uuid.UUID
	err := adminPool.QueryRow(ctx,
		`INSERT INTO tenants (slug, name) VALUES ($1, 'T') RETURNING id`, "t-"+uuid.NewString()[:8]).Scan(&tenantID)
	require.NoError(t, err)
	err = adminPool.QueryRow(ctx,
		`INSERT INTO users (tenant_id, email, password_hash) VALUES ($1, $2, 'x') RETURNING id`,
		tenantID, uuid.NewString()+"@t.test").Scan(&userID)
	require.NoError(t, err)
	return tenantID, userID
}

func containsAutomation(list []domain.Automation, id uuid.UUID) bool {
	for _, a := range list {
		if a.ID == id {
			return true
		}
	}
	return false
}

// captureTickets is a TicketCreator that records calls (stands in for Jira).
type captureTickets struct{ created []domain.TicketPayload }

func (c *captureTickets) CreateTicket(_ context.Context, _ domain.Identity, p domain.TicketPayload) (domain.TicketRef, error) {
	c.created = append(c.created, p)
	return domain.TicketRef{Provider: "jira", IssueKey: "NHI-1", URL: "http://j/NHI-1"}, nil
}

func TestAutomationRepository_CRUDAndClaim(t *testing.T) {
	ctx := context.Background()
	repo := store.NewPostgresAutomationRepository(appPool)
	tenantID, userID := seedAutomationOwner(t, ctx)

	a := &domain.Automation{
		TenantID: tenantID, OwnerUserID: userID, Name: "Blog", SiteURL: "https://x.com/blog",
		Provider: domain.ProviderJira, ProjectKey: "NHI", Interval: time.Hour, Enabled: true,
		NextScanAt: time.Now().Add(-time.Minute), // due
	}
	require.NoError(t, repo.Create(ctx, a))
	require.NotEqual(t, uuid.Nil, a.ID)

	got, err := repo.Get(ctx, tenantID, a.ID)
	require.NoError(t, err)
	require.Equal(t, "Blog", got.Name)
	require.Equal(t, time.Hour, got.Interval)

	list, err := repo.ListByTenant(ctx, tenantID)
	require.NoError(t, err)
	require.Len(t, list, 1)

	// Claim: the due row is returned and flipped to running.
	claimed, err := repo.ClaimDue(ctx, 10, 10*time.Minute)
	require.NoError(t, err)
	require.True(t, containsAutomation(claimed, a.ID))

	// A second claim does NOT return it again (now running, not past lease).
	again, err := repo.ClaimDue(ctx, 10, 10*time.Minute)
	require.NoError(t, err)
	require.False(t, containsAutomation(again, a.ID))

	// Complete reschedules and clears the lock.
	next := time.Now().Add(time.Hour)
	require.NoError(t, repo.Complete(ctx, tenantID, a.ID, next, ""))
	after, err := repo.Get(ctx, tenantID, a.ID)
	require.NoError(t, err)
	require.Equal(t, domain.AutomationIdle, after.Status)
	require.WithinDuration(t, next, after.NextScanAt, time.Second)

	require.NoError(t, repo.Delete(ctx, tenantID, a.ID))
	_, err = repo.Get(ctx, tenantID, a.ID)
	require.ErrorIs(t, err, domain.ErrAutomationNotFound)
}

func TestAutomationRepository_RLSIsolation(t *testing.T) {
	ctx := context.Background()
	repo := store.NewPostgresAutomationRepository(appPool)
	tA, uA := seedAutomationOwner(t, ctx)
	tB, _ := seedAutomationOwner(t, ctx)

	a := &domain.Automation{
		TenantID: tA, OwnerUserID: uA, Name: "A", SiteURL: "https://a/blog",
		Provider: domain.ProviderJira, ProjectKey: "NHI", Interval: time.Hour, Enabled: true,
		NextScanAt: time.Now(),
	}
	require.NoError(t, repo.Create(ctx, a))

	// Tenant B cannot see tenant A's automation.
	_, err := repo.Get(ctx, tB, a.ID)
	require.ErrorIs(t, err, domain.ErrAutomationNotFound)
	listB, err := repo.ListByTenant(ctx, tB)
	require.NoError(t, err)
	require.Empty(t, listB)
}

func TestAutomation_EndToEnd(t *testing.T) {
	ctx := context.Background()

	// A fake blog: sitemap with one post + the post HTML. Plus a fake Ollama.
	mux := http.NewServeMux()
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		_, _ = w.Write([]byte(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>` + base + `/blog/post-1</loc><lastmod>2026-01-01</lastmod></url>
</urlset>`))
	})
	mux.HandleFunc("/blog/post-1", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html><head><title>Post One</title></head><body><article>
		<h1>Post One</h1><p>Paragraph of meaningful body content long enough for extraction.
		Paragraph of meaningful body content long enough for extraction.</p></article></body></html>`))
	})
	mux.HandleFunc("/api/generate", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"response":"A short summary."}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	repo := store.NewPostgresAutomationRepository(appPool)
	seen := redisstore.NewRedisAutomationSeenSet(redisClient)
	tickets := &captureTickets{}
	svc := automation.NewService(automation.Deps{
		Repo:           repo,
		Disc:           discover.New(5 * time.Second),
		Scraper:        scrape.New(5 * time.Second),
		Summ:           summarize.New(srv.URL, "qwen2.5:0.5b", 5*time.Second, 8000),
		Tickets:        tickets,
		Seen:           seen,
		MaxPostsPerRun: 5,
	})

	tenantID, userID := seedAutomationOwner(t, ctx)
	a := domain.Automation{
		ID: uuid.New(), TenantID: tenantID, OwnerUserID: userID,
		SiteURL: srv.URL + "/blog", ProjectKey: "NHI", Provider: domain.ProviderJira,
	}

	// First run files exactly one ticket.
	require.NoError(t, svc.RunOnce(ctx, a))
	require.Len(t, tickets.created, 1)
	require.Equal(t, "Post One", tickets.created[0].Title)
	require.Contains(t, tickets.created[0].Description, "A short summary.")
	require.Contains(t, tickets.created[0].Description, "/blog/post-1")

	// Second run files nothing new (seen-set dedupes).
	require.NoError(t, svc.RunOnce(ctx, a))
	require.Len(t, tickets.created, 1)

	// cleanup seen set
	require.NoError(t, seen.Clear(ctx, a.ID))
}
