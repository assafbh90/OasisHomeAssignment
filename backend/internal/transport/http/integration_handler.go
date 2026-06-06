package http

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/samber/lo"

	"github.com/assafbh/identityhub/internal/domain"
)

const recentTicketsLimit = 10

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
	c.Redirect(http.StatusFound, h.redirectURL("connected", domain.ProviderJira))
}

// Status describes the current connection.
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
func (h *IntegrationHandler) Disconnect(c *gin.Context) {
	id, _ := mustIdentity(c)
	if err := h.conn.DisconnectIntegration(c.Request.Context(), id); err != nil {
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

// ListProjects returns the user's Jira projects (for the project picker).
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
