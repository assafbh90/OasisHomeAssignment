package app

import (
	"context"

	"github.com/assafbh/identityhub/internal/automation"
)

// RunScheduler runs the automation scheduler worker until ctx is cancelled. It
// reuses the same wiring as the API; only the entrypoint differs.
func (a *App) RunScheduler(ctx context.Context) error {
	sched := automation.NewScheduler(automation.SchedulerDeps{
		Service: a.autoSvc,
		Repo:    a.autoRepo,
		Tick:    a.cfg.Scheduler.Tick,
		Batch:   a.cfg.Scheduler.ClaimBatch,
		Lease:   a.cfg.Scheduler.Lease,
		Logger:  a.log,
	})
	a.log.Info("starting IdentityHub automation scheduler")
	err := sched.Run(ctx)
	if err == context.Canceled {
		return nil
	}
	return err
}
