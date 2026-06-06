package domain

import (
	"time"

	"github.com/google/uuid"
)

// AutomationStatus is the run state of an automation row. It doubles as the claim
// lock: the scheduler flips idle->running to take a row, so a new scan never
// overlaps a running one.
type AutomationStatus string

const (
	AutomationIdle    AutomationStatus = "idle"
	AutomationRunning AutomationStatus = "running"
)

// Automation watches a site and files a Jira ticket per newly-discovered post.
// It is tenant-scoped and visible to the whole tenant; OwnerUserID is the user
// whose Jira credential the worker uses to file tickets.
type Automation struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	OwnerUserID uuid.UUID
	Name        string
	SiteURL     string
	Provider    string
	ProjectKey  string
	Interval    time.Duration
	Enabled     bool
	Status      AutomationStatus
	NextScanAt  time.Time
	LockedAt    *time.Time
	LastRunAt   *time.Time
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
