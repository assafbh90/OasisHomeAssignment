package automation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/automation"
	"github.com/assafbh/identityhub/internal/domain"
)

// --- fakes ---

type fakeDisc struct{ urls []string }

func (f fakeDisc) Discover(context.Context, string) ([]string, error) { return f.urls, nil }

type fakeScraper struct{ err error }

func (f fakeScraper) Scrape(_ context.Context, u string) (string, string, error) {
	if f.err != nil {
		return "", "", f.err
	}
	return "Title of " + u, "# body " + u, nil
}

type fakeSumm struct{}

func (fakeSumm) Summarize(_ context.Context, pageTitle, md string) (domain.PostSummary, error) {
	return domain.PostSummary{Title: pageTitle, Source: "Acme Blog", Type: "blog", Body: "summary:" + md}, nil
}

type fakeTickets struct {
	created []domain.TicketPayload
	err     error
}

func (f *fakeTickets) CreateTicket(_ context.Context, _ domain.Identity, p domain.TicketPayload) (domain.TicketRef, error) {
	if f.err != nil {
		return domain.TicketRef{}, f.err
	}
	f.created = append(f.created, p)
	return domain.TicketRef{Provider: "jira", IssueKey: "NHI-1", URL: "http://j/NHI-1"}, nil
}

type fakeSeen struct{ added []string }

func (f *fakeSeen) Unseen(_ context.Context, _ uuid.UUID, urls []string) ([]string, error) {
	return urls, nil
}
func (f *fakeSeen) Add(_ context.Context, _ uuid.UUID, url string) error {
	f.added = append(f.added, url)
	return nil
}
func (f *fakeSeen) Clear(context.Context, uuid.UUID) error { return nil }

func testAutomation() domain.Automation {
	return domain.Automation{
		ID: uuid.New(), TenantID: uuid.New(), OwnerUserID: uuid.New(),
		SiteURL: "http://site/blog", ProjectKey: "NHI", Provider: domain.ProviderJira,
	}
}

func TestRunOnce_CreatesTicketsAndMarksSeen(t *testing.T) {
	t.Parallel()
	tickets := &fakeTickets{}
	seen := &fakeSeen{}
	svc := automation.NewService(automation.Deps{
		Disc:    fakeDisc{urls: []string{"http://site/blog/a", "http://site/blog/b"}},
		Scraper: fakeScraper{}, Summ: fakeSumm{}, Tickets: tickets, Seen: seen,
		MaxPostsPerRun: 0,
	})
	err := svc.RunOnce(context.Background(), testAutomation())
	require.NoError(t, err)
	require.Len(t, tickets.created, 2)
	require.Equal(t, "NHI", tickets.created[0].ProjectKey)
	require.Contains(t, tickets.created[0].Description, "summary:")
	require.Equal(t, []string{"http://site/blog/a", "http://site/blog/b"}, seen.added)
}

func TestRunOnce_RespectsCap(t *testing.T) {
	t.Parallel()
	tickets := &fakeTickets{}
	seen := &fakeSeen{}
	svc := automation.NewService(automation.Deps{
		Disc:    fakeDisc{urls: []string{"http://site/blog/a", "http://site/blog/b", "http://site/blog/c"}},
		Scraper: fakeScraper{}, Summ: fakeSumm{}, Tickets: tickets, Seen: seen,
		MaxPostsPerRun: 2,
	})
	require.NoError(t, svc.RunOnce(context.Background(), testAutomation()))
	require.Len(t, tickets.created, 2)
	require.Len(t, seen.added, 2)
}

func TestRunOnce_ReauthAbortsWithoutMarkingSeen(t *testing.T) {
	t.Parallel()
	tickets := &fakeTickets{err: domain.ErrReauthRequired}
	seen := &fakeSeen{}
	svc := automation.NewService(automation.Deps{
		Disc:    fakeDisc{urls: []string{"http://site/blog/a"}},
		Scraper: fakeScraper{}, Summ: fakeSumm{}, Tickets: tickets, Seen: seen,
	})
	err := svc.RunOnce(context.Background(), testAutomation())
	require.ErrorIs(t, err, domain.ErrReauthRequired)
	require.Empty(t, seen.added)
}

func TestRunOnce_ScrapeFailureSkipsPostButContinues(t *testing.T) {
	t.Parallel()
	tickets := &fakeTickets{}
	seen := &fakeSeen{}
	svc := automation.NewService(automation.Deps{
		Disc:    fakeDisc{urls: []string{"http://site/blog/a"}},
		Scraper: fakeScraper{err: errors.New("boom")}, Summ: fakeSumm{}, Tickets: tickets, Seen: seen,
	})
	require.NoError(t, svc.RunOnce(context.Background(), testAutomation()))
	require.Empty(t, tickets.created)
	require.Empty(t, seen.added) // not marked seen -> retried next run
}
