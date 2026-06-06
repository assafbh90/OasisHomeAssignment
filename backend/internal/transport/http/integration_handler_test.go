package http

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/domain"
)

type fakeConn struct {
	info          domain.ConnectionInfo
	startErr      error
	completeErr   error
	describeErr   error
	disconnectErr error
}

func (f fakeConn) StartAuthorization(context.Context, domain.Identity) (string, error) {
	return "https://auth.example.com/connect", f.startErr
}

func (f fakeConn) CompleteAuthorization(context.Context, domain.Identity, string, string) error {
	return f.completeErr
}

func (f fakeConn) DescribeConnection(context.Context, domain.Identity) (domain.ConnectionInfo, error) {
	return f.info, f.describeErr
}

func (f fakeConn) DisconnectIntegration(context.Context, domain.Identity) error {
	return f.disconnectErr
}

type fakeFindings struct {
	createErr    error
	projectsErr  error
	reconcileErr error
	ref          domain.TicketRef
}

func (f fakeFindings) ListProjects(context.Context, domain.Identity) ([]domain.ProjectRef, error) {
	if f.projectsErr != nil {
		return nil, f.projectsErr
	}
	return []domain.ProjectRef{{Key: "NHI", Name: "NHI"}}, nil
}

func (f fakeFindings) CreateTicket(context.Context, domain.Identity, domain.TicketPayload) (domain.TicketRef, error) {
	return f.ref, f.createErr
}

func (f fakeFindings) ListRecentTickets(context.Context, domain.Identity, string, int) ([]domain.CreatedTicket, error) {
	return nil, nil
}

func (f fakeFindings) Reconcile(context.Context, domain.Identity, bool) error {
	return f.reconcileErr
}

func integrationRouter(id domain.Identity, conn connectionService, find reportService) http.Handler {
	authH := NewAuthHandler(fakeAuthn{}, &fakeSession{}, fakeLimiter{allow: true}, testCookie())
	return newRouter(RouterDeps{
		Resolver:    fakeResolver{id: id},
		Auth:        authH,
		Tokens:      NewTokenHandler(&fakeTokens{}),
		Integration: NewIntegrationHandler(conn, find, "/"),
	})
}

func sessionID() domain.Identity {
	return domain.Identity{UserID: uuid.New(), TenantID: uuid.New(), AuthMethod: domain.AuthMethodSession}
}

func csrf() ([]*http.Cookie, map[string]string) {
	return []*http.Cookie{{Name: csrfCookieName, Value: "t"}}, map[string]string{headerCSRFToken: "t"}
}

func TestIntegration_UnknownProvider(t *testing.T) {
	r := integrationRouter(sessionID(), fakeConn{}, fakeFindings{})
	rec := doJSON(r, http.MethodGet, "/v1/integrations/github/status", nil, nil, nil)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestIntegration_CreateTicket_Success(t *testing.T) {
	find := fakeFindings{ref: domain.TicketRef{Provider: "jira", IssueKey: "NHI-1", URL: "https://acme.atlassian.net/browse/NHI-1"}}
	r := integrationRouter(sessionID(), fakeConn{}, find)
	cookies, headers := csrf()
	rec := doJSON(r, http.MethodPost, "/v1/integrations/jira/tickets",
		ticketRequest{ProjectKey: "NHI", Title: "Stale account"}, cookies, headers)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
}

func TestIntegration_CreateTicket_Reauth409(t *testing.T) {
	find := fakeFindings{createErr: domain.ErrReauthRequired}
	r := integrationRouter(sessionID(), fakeConn{}, find)
	cookies, headers := csrf()
	rec := doJSON(r, http.MethodPost, "/v1/integrations/jira/tickets",
		ticketRequest{ProjectKey: "NHI", Title: "x"}, cookies, headers)

	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "reauth_required")
}

func TestIntegration_Reconcile_OK(t *testing.T) {
	r := integrationRouter(sessionID(), fakeConn{}, fakeFindings{})
	cookies, headers := csrf()
	rec := doJSON(r, http.MethodPost, "/v1/integrations/jira/reconcile", nil, cookies, headers)
	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestIntegration_Reconcile_Reauth409(t *testing.T) {
	r := integrationRouter(sessionID(), fakeConn{}, fakeFindings{reconcileErr: domain.ErrReauthRequired})
	cookies, headers := csrf()
	rec := doJSON(r, http.MethodPost, "/v1/integrations/jira/reconcile", nil, cookies, headers)
	require.Equal(t, http.StatusConflict, rec.Code)
	require.Contains(t, rec.Body.String(), "reauth_required")
}

func TestIntegration_CreateTicket_RequiresWriteScope(t *testing.T) {
	// A machine token lacking integrations:write must be rejected with 403.
	id := domain.Identity{UserID: uuid.New(), TenantID: uuid.New(), AuthMethod: domain.AuthMethodToken, Scopes: []string{domain.ScopeIntegrationsRead}}
	r := integrationRouter(id, fakeConn{}, fakeFindings{})
	rec := doJSON(r, http.MethodPost, "/v1/integrations/jira/tickets",
		ticketRequest{ProjectKey: "NHI", Title: "x"}, nil, nil)
	require.Equal(t, http.StatusForbidden, rec.Code)
}

func TestIntegration_RecentTickets_RequiresProject(t *testing.T) {
	r := integrationRouter(sessionID(), fakeConn{}, fakeFindings{})
	rec := doJSON(r, http.MethodGet, "/v1/integrations/jira/tickets", nil, nil, nil)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestIntegration_Connect_ReturnsAuthURL(t *testing.T) {
	r := integrationRouter(sessionID(), fakeConn{}, fakeFindings{})
	rec := doJSON(r, http.MethodGet, "/v1/integrations/jira/connect", nil, nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "auth_url")
}
