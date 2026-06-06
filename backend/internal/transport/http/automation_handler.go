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
	automations automationService
}

// NewAutomationHandler constructs the handler.
func NewAutomationHandler(automations automationService) *AutomationHandler {
	return &AutomationHandler{automations: automations}
}

// List returns the tenant's automations.
//
// @Summary  List automations (blog-digest watchers)
// @Tags     automations
// @Security CookieAuth
// @Security BearerAuth
// @Produce  json
// @Success  200  {object}  map[string][]automationResponse
// @Router   /v1/automations [get]
func (h *AutomationHandler) List(c *gin.Context) {
	id, _ := mustIdentity(c)
	items, err := h.automations.List(c.Request.Context(), id)
	if err != nil {
		respondError(c, err)
		return
	}
	out := lo.Map(items, func(a domain.Automation, _ int) automationResponse { return toAutomationResponse(a) })
	c.JSON(http.StatusOK, gin.H{"automations": out})
}

// Create makes a new automation.
//
// @Summary  Create an automation
// @Tags     automations
// @Security CookieAuth
// @Security BearerAuth
// @Accept   json
// @Produce  json
// @Param    automation  body      automationRequest  true  "Automation"
// @Success  201         {object}  automationResponse
// @Failure  400         {object}  errorResponse
// @Router   /v1/automations [post]
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
	a, err := h.automations.Create(c.Request.Context(), id, automation.CreateInput{
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
//
// @Summary  Get an automation
// @Tags     automations
// @Security CookieAuth
// @Security BearerAuth
// @Produce  json
// @Param    id   path      string  true  "Automation ID"
// @Success  200  {object}  automationResponse
// @Failure  404  {object}  errorResponse
// @Router   /v1/automations/{id} [get]
func (h *AutomationHandler) Get(c *gin.Context) {
	id, _ := mustIdentity(c)
	aid, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondValidation(c, "invalid automation id")
		return
	}
	a, err := h.automations.Get(c.Request.Context(), id, aid)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAutomationResponse(a))
}

// Update applies field changes.
//
// @Summary  Update an automation (full replacement)
// @Tags     automations
// @Security CookieAuth
// @Security BearerAuth
// @Accept   json
// @Produce  json
// @Param    id          path      string             true  "Automation ID"
// @Param    automation  body      automationRequest  true  "Automation"
// @Success  200         {object}  automationResponse
// @Failure  400         {object}  errorResponse
// @Failure  404         {object}  errorResponse
// @Router   /v1/automations/{id} [put]
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
		interval := time.Duration(req.IntervalSeconds) * time.Second
		in.Interval = &interval
	}
	a, err := h.automations.Update(c.Request.Context(), id, aid, in)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toAutomationResponse(a))
}

// Delete removes an automation.
//
// @Summary  Delete an automation (and clear its seen-set)
// @Tags     automations
// @Security CookieAuth
// @Security BearerAuth
// @Param    id   path  string  true  "Automation ID"
// @Success  204
// @Failure  404  {object}  errorResponse
// @Router   /v1/automations/{id} [delete]
func (h *AutomationHandler) Delete(c *gin.Context) {
	id, _ := mustIdentity(c)
	aid, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondValidation(c, "invalid automation id")
		return
	}
	if err := h.automations.Delete(c.Request.Context(), id, aid); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// RunNow makes an automation due immediately.
//
// @Summary  Run an automation now (schedule on the next tick)
// @Tags     automations
// @Security CookieAuth
// @Security BearerAuth
// @Param    id   path  string  true  "Automation ID"
// @Success  202
// @Failure  404  {object}  errorResponse
// @Router   /v1/automations/{id}/run [post]
func (h *AutomationHandler) RunNow(c *gin.Context) {
	id, _ := mustIdentity(c)
	aid, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondValidation(c, "invalid automation id")
		return
	}
	if err := h.automations.RunNow(c.Request.Context(), id, aid); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusAccepted)
}
