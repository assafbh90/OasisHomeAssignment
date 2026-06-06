package automation

import (
	"context"
	"log/slog"
	"time"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/logging"
)

// schedRunner is the slice of Service the scheduler needs.
type schedRunner interface {
	RunOnce(ctx context.Context, a domain.Automation) error
}

// Scheduler claims due automations and runs them one at a time (serial Ollama
// calls bound memory), then schedules the next scan.
type Scheduler struct {
	svc   schedRunner
	repo  Repository
	tick  time.Duration
	batch int
	lease time.Duration
	now   func() time.Time
	log   *slog.Logger
}

// SchedulerDeps are the collaborators of a Scheduler.
type SchedulerDeps struct {
	Service schedRunner
	Repo    Repository
	Tick    time.Duration
	Batch   int
	Lease   time.Duration
	Now     func() time.Time
	Logger  *slog.Logger
}

// NewScheduler constructs the scheduler.
func NewScheduler(d SchedulerDeps) *Scheduler {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	log := d.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{svc: d.Service, repo: d.Repo, tick: d.Tick, batch: d.Batch, lease: d.Lease, now: now, log: log}
}

// Run loops until ctx is cancelled, polling for due automations each tick.
func (s *Scheduler) Run(ctx context.Context) error {
	s.log.Info("automation scheduler started", slog.Duration("tick", s.tick))
	t := time.NewTicker(s.tick)
	defer t.Stop()
	for {
		s.RunDue(ctx)
		select {
		case <-ctx.Done():
			s.log.Info("automation scheduler stopping")
			return ctx.Err()
		case <-t.C:
		}
	}
}

// RunDue claims and processes one batch of due automations. Exposed for tests.
func (s *Scheduler) RunDue(ctx context.Context) {
	claimed, err := s.repo.ClaimDue(ctx, s.batch, s.lease)
	if err != nil {
		s.log.Error("claim due automations failed", logging.Err(err))
		return
	}
	for _, a := range claimed {
		runErr := s.svc.RunOnce(ctx, a)
		msg := ""
		if runErr != nil {
			msg = runErr.Error()
			s.log.Warn("automation run failed", slog.String("automation_id", a.ID.String()), logging.Err(runErr))
		}
		next := s.now().Add(a.Interval)
		if err := s.repo.Complete(ctx, a.TenantID, a.ID, next, msg); err != nil {
			s.log.Error("complete automation failed", slog.String("automation_id", a.ID.String()), logging.Err(err))
		}
	}
}
