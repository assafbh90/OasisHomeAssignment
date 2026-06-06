package http

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/samber/lo"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/logging"
)

const (
	recentTicketsLimit = 10
	// reconcileTimeout bounds the async post-connect reconcile.
	reconcileTimeout = 30 * time.Second
)

// connectionService is the slice of the integration ConnectionService the
// handler needs. Provider is implicit (Jira-only) — validated by middleware.
type connectionService interface {
	StartAuthorization(ctx context.Context, principal domain.Identity) (authURL string, err error)
	CompleteAuthorization(ctx context.Context, principal domain.Identity, state, code string) error
	DescribeConnection(ctx context.Context, principal domain.Identity) (domain.ConnectionInfo, error)
	DisconnectIntegration(ctx context.Context, principal domain.Identity) error
}

// reportService is the slice of the NHI ticketreport.Service the handler needs.
type reportService interface {
	ListProjects(ctx context.Context, principal domain.Identity) ([]domain.ProjectRef, error)
	CreateTicket(ctx context.Context, principal domain.Identity, payload domain.TicketPayload) (domain.TicketRef, error)
	ListRecentTickets(ctx context.Context, principal domain.Identity, projectKey string, limit int) ([]domain.CreatedTicket, error)
	Reconcile(ctx context.Context, principal domain.Identity, force bool) error
}

// IntegrationHandler serves the Jira integration endpoints. It never branches on
// provider name; the {provider} segment is validated by ValidateProvider.
type IntegrationHandler struct {
	conn    connectionService
	reports reportService

	// frontendPostConnectPath is where the OAuth callback redirects the browser
	// after completing (or failing) the connection.
	frontendPostConnectPath string
}

// NewIntegrationHandler constructs the handler.
func NewIntegrationHandler(conn connectionService, reports reportService, postConnectPath string) *IntegrationHandler {
	if postConnectPath == "" {
		postConnectPath = "/"
	}
	return &IntegrationHandler{conn: conn, reports: reports, frontendPostConnectPath: postConnectPath}
}

// ValidateProvider rejects unknown providers with 404. Only Jira is supported.
func ValidateProvider() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Param("provider") != domain.ProviderJira {
			respondError(c, domain.ErrProviderNotSupported)
			c.Abort()
			return
		}
		c.Next()
	}
}

// ListIntegrations returns connection info for each provider (just Jira here).
//
// @Summary  List integrations (connection status)
// @Tags     integrations
// @Security CookieAuth
// @Security BearerAuth
// @Produce  json
// @Success  200  {object}  map[string][]connectionResponse
// @Router   /v1/integrations [get]
func (h *IntegrationHandler) ListIntegrations(c *gin.Context) {
	id, _ := mustIdentity(c)
	info, err := h.conn.DescribeConnection(c.Request.Context(), id)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"integrations": []connectionResponse{toConnectionResponse(info)}})
}

// Connect starts the OAuth authorization and returns the provider auth URL.
//
// @Summary  Start Jira OAuth (returns the consent URL)
// @Tags     integrations
// @Security CookieAuth
// @Produce  json
// @Param    provider  path      string  true  "Provider"  default(jira)
// @Success  200       {object}  authURLResponse
// @Failure  401       {object}  errorResponse
// @Router   /v1/integrations/{provider}/connect [get]
func (h *IntegrationHandler) Connect(c *gin.Context) {
	id, _ := mustIdentity(c)
	url, err := h.conn.StartAuthorization(c.Request.Context(), id)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, authURLResponse{AuthURL: url})
}

// Callback completes the OAuth flow and redirects the browser back to the SPA.
//
// @Summary  Jira OAuth callback (redirects to the SPA)
// @Tags     integrations
// @Security CookieAuth
// @Param    provider  path   string  true  "Provider"  default(jira)
// @Param    state     query  string  true  "OAuth state"
// @Param    code      query  string  true  "Authorization code"
// @Success  302
// @Router   /v1/integrations/{provider}/callback [get]
func (h *IntegrationHandler) Callback(c *gin.Context) {
	id, _ := mustIdentity(c)
	state := c.Query("state")
	code := c.Query("code")
	if state == "" || code == "" {
		c.Redirect(http.StatusFound, h.redirectURL("connect_error", "missing_params"))
		return
	}
	if err := h.conn.CompleteAuthorization(c.Request.Context(), id, state, code); err != nil {
		c.Redirect(http.StatusFound, h.redirectURL("connect_error", "1"))
		return
	}
	// Refresh the tenant's ticket cache from Jira after connecting (async,
	// throttled by the gate), so a fresh user immediately sees existing tickets.
	h.reconcileAsync(c.Request.Context(), id)
	c.Redirect(http.StatusFound, h.redirectURL("connected", domain.ProviderJira))
}

// Reconcile forces a refresh of the tenant's ticket cache from Jira (the refresh
// button). It is single-flighted by the gate; reauth needs surface as 409.
//
// @Summary  Reconcile the ticket cache from Jira (drift refresh)
// @Tags     integrations
// @Security CookieAuth
// @Security BearerAuth
// @Param    provider  path  string  true  "Provider"  default(jira)
// @Success  204
// @Failure  409  {object}  errorResponse  "reauth_required"
// @Router   /v1/integrations/{provider}/reconcile [post]
func (h *IntegrationHandler) Reconcile(c *gin.Context) {
	id, _ := mustIdentity(c)
	if err := h.reports.Reconcile(c.Request.Context(), id, true); err != nil {
		h.respondIntegrationError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// reconcileAsync runs a throttled reconcile in the background on a detached
// context, so it survives the OAuth callback's redirect.
func (h *IntegrationHandler) reconcileAsync(reqCtx context.Context, id domain.Identity) {
	ctx := context.WithoutCancel(reqCtx)
	go func() {
		ctx, cancel := context.WithTimeout(ctx, reconcileTimeout)
		defer cancel()
		if err := h.reports.Reconcile(ctx, id, false); err != nil {
			logging.FromContext(ctx).Warn("post-connect reconcile failed", logging.Err(err))
		}
	}()
}

// Status describes the current connection.
//
// @Summary  Connection status
// @Tags     integrations
// @Security CookieAuth
// @Security BearerAuth
// @Produce  json
// @Param    provider  path      string  true  "Provider"  default(jira)
// @Success  200       {object}  connectionResponse
// @Router   /v1/integrations/{provider}/status [get]
func (h *IntegrationHandler) Status(c *gin.Context) {
	id, _ := mustIdentity(c)
	info, err := h.conn.DescribeConnection(c.Request.Context(), id)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, toConnectionResponse(info))
}

// Disconnect removes the connection.
//
// @Summary  Disconnect the integration
// @Tags     integrations
// @Security CookieAuth
// @Security BearerAuth
// @Param    provider  path  string  true  "Provider"  default(jira)
// @Success  204
// @Failure  403  {object}  errorResponse
// @Router   /v1/integrations/{provider} [delete]
func (h *IntegrationHandler) Disconnect(c *gin.Context) {
	id, _ := mustIdentity(c)
	if err := h.conn.DisconnectIntegration(c.Request.Context(), id); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ListProjects returns the user's Jira projects (for the project picker).
//
// @Summary  List Jira projects (project picker)
// @Tags     integrations
// @Security CookieAuth
// @Security BearerAuth
// @Produce  json
// @Param    provider  path      string  true  "Provider"  default(jira)
// @Success  200       {object}  map[string][]projectResponse
// @Failure  409       {object}  errorResponse  "reauth_required"
// @Router   /v1/integrations/{provider}/projects [get]
func (h *IntegrationHandler) ListProjects(c *gin.Context) {
	id, _ := mustIdentity(c)
	projects, err := h.reports.ListProjects(c.Request.Context(), id)
	if err != nil {
		h.respondIntegrationError(c, err)
		return
	}
	out := lo.Map(projects, func(p domain.ProjectRef, _ int) projectResponse {
		return projectResponse{Key: p.Key, Name: p.Name}
	})
	c.JSON(http.StatusOK, gin.H{"projects": out})
}

// CreateTicket creates an NHI finding ticket (UI session or REST API key).
//
// @Summary  Create an NHI finding ticket (tagged identityhub)
// @Tags     integrations
// @Security CookieAuth
// @Security BearerAuth
// @Accept   json
// @Produce  json
// @Param    provider  path      string         true  "Provider"  default(jira)
// @Param    ticket    body      ticketRequest  true  "Finding"
// @Success  201       {object}  ticketResponse
// @Failure  400       {object}  errorResponse
// @Failure  403       {object}  errorResponse
// @Failure  409       {object}  errorResponse  "reauth_required"
// @Router   /v1/integrations/{provider}/tickets [post]
func (h *IntegrationHandler) CreateTicket(c *gin.Context) {
	id, _ := mustIdentity(c)
	var req ticketRequest
	if err := bindJSON(c, &req); err != nil {
		respondValidation(c, "invalid request body")
		return
	}
	if msg := req.validate(); msg != "" {
		respondValidation(c, msg)
		return
	}

	ref, err := h.reports.CreateTicket(c.Request.Context(), id, domain.TicketPayload{
		ProjectKey:  req.ProjectKey,
		Title:       req.Title,
		Description: req.Description,
		Labels:      req.Labels,
	})
	if err != nil {
		h.respondIntegrationError(c, err)
		return
	}
	c.JSON(http.StatusCreated, ticketResponse{Provider: ref.Provider, IssueKey: ref.IssueKey, URL: ref.URL})
}

// ListRecentTickets returns the 10 most recent app-created tickets for a project.
//
// @Summary  Recent IdentityHub tickets (cached)
// @Tags     integrations
// @Security CookieAuth
// @Security BearerAuth
// @Produce  json
// @Param    provider  path      string  true  "Provider"  default(jira)
// @Param    project   query     string  true  "Project key"
// @Success  200       {object}  map[string][]recentTicketResponse
// @Failure  400       {object}  errorResponse
// @Router   /v1/integrations/{provider}/tickets [get]
func (h *IntegrationHandler) ListRecentTickets(c *gin.Context) {
	id, _ := mustIdentity(c)
	project := strings.TrimSpace(c.Query("project"))
	if project == "" {
		respondValidation(c, "project query parameter is required")
		return
	}
	tickets, err := h.reports.ListRecentTickets(c.Request.Context(), id, project, recentTicketsLimit)
	if err != nil {
		h.respondIntegrationError(c, err)
		return
	}
	out := lo.Map(tickets, func(t domain.CreatedTicket, _ int) recentTicketResponse {
		return recentTicketResponse{
			IssueKey:   t.IssueKey,
			Title:      t.Title,
			URL:        t.IssueURL,
			ProjectKey: t.ProjectKey,
			CreatedAt:  t.CreatedAt.UTC().Format(rfc3339),
		}
	})
	c.JSON(http.StatusOK, gin.H{"tickets": out})
}

// respondIntegrationError maps the reauth-required signal to a 409 telling the
// client to reconnect, and defers everything else to respondError.
func (h *IntegrationHandler) respondIntegrationError(c *gin.Context, err error) {
	if errors.Is(err, domain.ErrReauthRequired) {
		c.JSON(http.StatusConflict, errorResponse{
			Error:   errCodeReauthRequired,
			Message: "the integration must be reconnected — start a new connection",
		})
		return
	}
	respondError(c, err)
}

func (h *IntegrationHandler) redirectURL(key, value string) string {
	sep := "?"
	if strings.Contains(h.frontendPostConnectPath, "?") {
		sep = "&"
	}
	return h.frontendPostConnectPath + sep + key + "=" + value
}

func toConnectionResponse(info domain.ConnectionInfo) connectionResponse {
	return connectionResponse{
		Provider:  info.Provider,
		Status:    string(info.Status),
		Connected: info.Connected,
		Scopes:    emptyIfNil(info.Scopes),
	}
}
