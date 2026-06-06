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
	RunOnce(ctx context.Context, a domain.Automation) (RunResult, error)
}

// Scheduler claims due automations and runs them one at a time (serial Ollama
// calls bound memory), then schedules the next scan: soon (drain) while a backlog
// remains, else at the automation's steady-state interval.
type Scheduler struct {
	runner    schedRunner
	repo      Repository
	tick      time.Duration
	batchSize int
	lease     time.Duration
	drain     time.Duration
	now       func() time.Time
	log       *slog.Logger
}

// SchedulerDeps are the collaborators of a Scheduler.
type SchedulerDeps struct {
	Service schedRunner
	Repo    Repository
	Tick    time.Duration
	Batch   int
	Lease   time.Duration
	Drain   time.Duration // short reschedule while a backlog remains
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
	return &Scheduler{
		runner: d.Service, repo: d.Repo, tick: d.Tick, batchSize: d.Batch,
		lease: d.Lease, drain: d.Drain, now: now, log: log,
	}
}

// Run loops until ctx is cancelled, polling for due automations each tick.
func (s *Scheduler) Run(ctx context.Context) error {
	s.log.Info("automation scheduler started", slog.Duration("tick", s.tick))
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()
	for {
		s.RunDue(ctx)
		select {
		case <-ctx.Done():
			s.log.Info("automation scheduler stopping")
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// RunDue claims and processes one batch of due automations. Exposed for tests.
func (s *Scheduler) RunDue(ctx context.Context) {
	claimed, err := s.repo.ClaimDue(ctx, s.batchSize, s.lease)
	if err != nil {
		s.log.Error("claim due automations failed", logging.Err(err))
		return
	}
	if len(claimed) > 0 {
		s.log.Debug("automation batch claimed", slog.Int("count", len(claimed)))
	}
	for _, automation := range claimed {
		runLog := s.log.With(slog.String(logging.KeyAutomationID, automation.ID.String()))
		result, runErr := s.runner.RunOnce(ctx, automation)
		runErrMessage := ""
		if runErr != nil {
			runErrMessage = runErr.Error()
			runLog.Warn("automation run failed", logging.Err(runErr))
		}
		// Drain a backlog fast (short reschedule); otherwise — caught up, or a hard
		// failure we should back off from — wait the steady-state interval.
		interval := automation.Interval
		draining := runErr == nil && result.Backlog && s.drain > 0
		if draining {
			interval = s.drain
		}
		next := s.now().Add(interval)
		if err := s.repo.Complete(ctx, automation.TenantID, automation.ID, next, runErrMessage); err != nil {
			runLog.Error("complete automation failed", logging.Err(err))
			continue
		}
		runLog.Debug("automation rescheduled",
			slog.Bool("draining", draining),
			slog.Time("next_scan_at", next))
	}
}
