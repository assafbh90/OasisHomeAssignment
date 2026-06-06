package automation_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/automation"
	"github.com/assafbh/identityhub/internal/domain"
)

// fakeRepo records ClaimDue/Complete. ClaimDue returns its queued batch once,
// then nothing.
type fakeRepo struct {
	mu        sync.Mutex
	batches   [][]domain.Automation
	completed []completion
}

type completion struct {
	id     uuid.UUID
	next   time.Time
	runErr string
}

func (r *fakeRepo) ClaimDue(_ context.Context, _ int, _ time.Duration) ([]domain.Automation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.batches) == 0 {
		return nil, nil
	}
	b := r.batches[0]
	r.batches = r.batches[1:]
	return b, nil
}

func (r *fakeRepo) Complete(_ context.Context, _, id uuid.UUID, next time.Time, runErr string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completed = append(r.completed, completion{id: id, next: next, runErr: runErr})
	return nil
}

// unused Repository methods (scheduler only needs ClaimDue/Complete).
func (r *fakeRepo) Create(context.Context, *domain.Automation) error { return nil }
func (r *fakeRepo) Get(context.Context, uuid.UUID, uuid.UUID) (domain.Automation, error) {
	return domain.Automation{}, nil
}
func (r *fakeRepo) ListByTenant(context.Context, uuid.UUID) ([]domain.Automation, error) {
	return nil, nil
}
func (r *fakeRepo) Update(context.Context, *domain.Automation) error              { return nil }
func (r *fakeRepo) Delete(context.Context, uuid.UUID, uuid.UUID) error            { return nil }
func (r *fakeRepo) SetDue(context.Context, uuid.UUID, uuid.UUID, time.Time) error { return nil }

func TestScheduler_RunDue_RunsAndCompletes(t *testing.T) {
	t.Parallel()
	a := testAutomation()
	a.Interval = time.Hour
	repo := &fakeRepo{batches: [][]domain.Automation{{a}}}
	tickets := &fakeTickets{}
	svc := automation.NewService(automation.Deps{
		Disc:    fakeDisc{urls: []string{"http://site/blog/a"}},
		Scraper: fakeScraper{}, Summ: fakeSumm{}, Tickets: tickets, Seen: &fakeSeen{},
	})
	fixedNow := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	sch := automation.NewScheduler(automation.SchedulerDeps{
		Service: svc, Repo: repo, Tick: time.Hour, Batch: 5, Lease: time.Minute,
		Now: func() time.Time { return fixedNow },
	})

	sch.RunDue(context.Background()) // one pass

	require.Len(t, tickets.created, 1)
	require.Len(t, repo.completed, 1)
	require.Equal(t, a.ID, repo.completed[0].id)
	require.Equal(t, fixedNow.Add(time.Hour), repo.completed[0].next)
	require.Empty(t, repo.completed[0].runErr)
}

func TestScheduler_RunDue_DrainsBacklogFast(t *testing.T) {
	t.Parallel()
	a := testAutomation()
	a.Interval = time.Hour
	repo := &fakeRepo{batches: [][]domain.Automation{{a}}}
	// 3 posts, cap 2 -> a backlog remains, so the next scan uses the drain window.
	svc := automation.NewService(automation.Deps{
		Disc:    fakeDisc{urls: []string{"http://site/blog/a", "http://site/blog/b", "http://site/blog/c"}},
		Scraper: fakeScraper{}, Summ: fakeSumm{}, Tickets: &fakeTickets{}, Seen: &fakeSeen{},
		MaxPostsPerRun: 2,
	})
	fixedNow := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	sch := automation.NewScheduler(automation.SchedulerDeps{
		Service: svc, Repo: repo, Tick: time.Hour, Batch: 5, Lease: time.Minute,
		Drain: 15 * time.Second, Now: func() time.Time { return fixedNow },
	})

	sch.RunDue(context.Background())

	require.Len(t, repo.completed, 1)
	require.Equal(t, fixedNow.Add(15*time.Second), repo.completed[0].next) // drain, not the 1h interval
}

func TestScheduler_RunDue_RecordsError(t *testing.T) {
	t.Parallel()
	a := testAutomation()
	a.Interval = time.Hour
	repo := &fakeRepo{batches: [][]domain.Automation{{a}}}
	svc := automation.NewService(automation.Deps{
		Disc:    fakeDisc{urls: []string{"http://site/blog/a"}},
		Scraper: fakeScraper{}, Summ: fakeSumm{},
		Tickets: &fakeTickets{err: domain.ErrReauthRequired}, Seen: &fakeSeen{},
	})
	sch := automation.NewScheduler(automation.SchedulerDeps{
		Service: svc, Repo: repo, Tick: time.Hour, Batch: 5, Lease: time.Minute,
		Now: func() time.Time { return time.Unix(0, 0) },
	})
	sch.RunDue(context.Background())
	require.Len(t, repo.completed, 1)
	require.Contains(t, repo.completed[0].runErr, "reauth")
}
