package http

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/samber/lo"

	"github.com/assafbh/identityhub/internal/automation"
	"github.com/assafbh/identityhub/internal/domain"
)

// automationService is the slice of automation.Service the handler needs.
type automationService interface {
	Create(ctx context.Context, principal domain.Identity, in automation.CreateInput) (domain.Automation, error)
	List(ctx context.Context, principal domain.Identity) ([]domain.Automation, error)
	Get(ctx context.Context, principal domain.Identity, id uuid.UUID) (domain.Automation, error)
	Update(ctx context.Context, principal domain.Identity, id uuid.UUID, in automation.UpdateInput) (domain.Automation, error)
	Delete(ctx context.Context, principal domain.Identity, id uuid.UUID) error
	RunNow(ctx context.Context, principal domain.Identity, id uuid.UUID) error
}

// AutomationHandler serves the automation CRUD + run-now endpoints.
type AutomationHandler struct {
	svc automationService
}

// NewAutomationHandler constructs the handler.
func NewAutomationHandler(svc automationService) *AutomationHandler {
	return &AutomationHandler{svc: svc}
}

// List returns the tenant's automations.
func (h *AutomationHandler) List(c *gin.Context) {
	id, _ := mustIdentity(c)
	items, err := h.svc.List(c.Request.Context(), id)
	if err != nil {
		respondError(c, err)
		return
	}
	out := lo.Map(items, func(a domain.Automation, _ int) automationResponse { return toAutomationResponse(a) })
	c.JSON(http.StatusOK, gin.H{"automations": out})
}

// Create makes a new automation.
func (h *AutomationHandler) Create(c *gin.Context) {
	id, _ := mustIdentity(c)
	var req automationRequest
	if err := bindJSON(c, &req); err != nil {
		respondValidation(c, "invalid request body")
		return
	}
	if msg := req.validate(); msg != "" {
		respondValidation(c, msg)
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	a, err := h.svc.Create(c.Request.Context(), id, automation.CreateInput{
		Name:       req.Name,
		SiteURL:    req.SiteURL,
		ProjectKey: req.ProjectKey,
		Interval:   time.Duration(req.IntervalSeconds) * time.Second,
		Enabled:    enabled,
	})
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusCreated, toAutomationResponse(a))
}

// Get returns one automation.
func (h *AutomationHandler) Get(c *gin.Context) {
	id, _ := mustIdentity(c)
	aid, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondValidation(c, "invalid automation id")
		return
	}
	a, err := h.svc.Get(c.Request.Context(), id, aid)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAutomationResponse(a))
}

// Update applies field changes.
func (h *AutomationHandler) Update(c *gin.Context) {
	id, _ := mustIdentity(c)
	aid, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondValidation(c, "invalid automation id")
		return
	}
	var req automationRequest
	if err := bindJSON(c, &req); err != nil {
		respondValidation(c, "invalid request body")
		return
	}
	if msg := req.validate(); msg != "" {
		respondValidation(c, msg)
		return
	}
	in := automation.UpdateInput{Name: &req.Name, SiteURL: &req.SiteURL, ProjectKey: &req.ProjectKey, Enabled: req.Enabled}
	if req.IntervalSeconds != 0 {
		d := time.Duration(req.IntervalSeconds) * time.Second
		in.Interval = &d
	}
	a, err := h.svc.Update(c.Request.Context(), id, aid, in)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAutomationResponse(a))
}

// Delete removes an automation.
func (h *AutomationHandler) Delete(c *gin.Context) {
	id, _ := mustIdentity(c)
	aid, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondValidation(c, "invalid automation id")
		return
	}
	if err := h.svc.Delete(c.Request.Context(), id, aid); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// RunNow makes an automation due immediately.
func (h *AutomationHandler) RunNow(c *gin.Context) {
	id, _ := mustIdentity(c)
	aid, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondValidation(c, "invalid automation id")
		return
	}
	if err := h.svc.RunNow(c.Request.Context(), id, aid); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusAccepted)
}
