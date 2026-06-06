package http

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/domain"
)

// rawPost sends a raw (possibly malformed) body with CSRF, for bind-error paths.
func rawPost(r http.Handler, path, body string, cookies []*http.Cookie, headers map[string]string) int {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code
}

func TestValidationErrors(t *testing.T) {
	cookies, headers := csrf()

	t.Run("login malformed json", func(t *testing.T) {
		r := newRouter(RouterDeps{Resolver: fakeResolver{}, Auth: minimalAuth(), Tokens: minimalTokens()})
		require.Equal(t, http.StatusBadRequest, rawPost(r, "/v1/auth/login", "{bad", nil, nil))
	})

	t.Run("token issue empty name", func(t *testing.T) {
		r := newRouter(RouterDeps{Resolver: fakeResolver{id: sessionID()}, Auth: minimalAuth(), Tokens: minimalTokens()})
		rec := doJSON(r, http.MethodPost, "/v1/tokens", issueTokenRequest{Name: ""}, cookies, headers)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("token issue unknown scope", func(t *testing.T) {
		r := newRouter(RouterDeps{Resolver: fakeResolver{id: sessionID()}, Auth: minimalAuth(), Tokens: minimalTokens()})
		rec := doJSON(r, http.MethodPost, "/v1/tokens", issueTokenRequest{Name: "x", Scopes: []string{"bogus"}}, cookies, headers)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("create ticket missing title", func(t *testing.T) {
		r := integrationRouter(sessionID(), fakeConn{}, fakeFindings{})
		rec := doJSON(r, http.MethodPost, "/v1/integrations/jira/tickets", ticketRequest{ProjectKey: "NHI"}, cookies, headers)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("create ticket malformed json", func(t *testing.T) {
		r := integrationRouter(sessionID(), fakeConn{}, fakeFindings{})
		require.Equal(t, http.StatusBadRequest, rawPost(r, "/v1/integrations/jira/tickets", "{bad", cookies, headers))
	})

	t.Run("revoke invalid token id", func(t *testing.T) {
		r := newRouter(RouterDeps{Resolver: fakeResolver{id: sessionID()}, Auth: minimalAuth(), Tokens: minimalTokens()})
		rec := doJSON(r, http.MethodDelete, "/v1/tokens/not-a-uuid", nil, cookies, headers)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})
}

func TestIntegrationHandler_ServiceErrors(t *testing.T) {
	cookies, headers := csrf()
	boom := errors.New("downstream boom")

	t.Run("connect error", func(t *testing.T) {
		r := integrationRouter(sessionID(), fakeConn{startErr: boom}, fakeFindings{})
		rec := doJSON(r, http.MethodGet, "/v1/integrations/jira/connect", nil, nil, nil)
		require.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("status error", func(t *testing.T) {
		r := integrationRouter(sessionID(), fakeConn{describeErr: boom}, fakeFindings{})
		rec := doJSON(r, http.MethodGet, "/v1/integrations/jira/status", nil, nil, nil)
		require.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("disconnect error", func(t *testing.T) {
		r := integrationRouter(sessionID(), fakeConn{disconnectErr: boom}, fakeFindings{})
		rec := doJSON(r, http.MethodDelete, "/v1/integrations/jira", nil, cookies, headers)
		require.Equal(t, http.StatusInternalServerError, rec.Code)
	})

	t.Run("projects not connected", func(t *testing.T) {
		r := integrationRouter(sessionID(), fakeConn{}, fakeFindings{projectsErr: domain.ErrCredentialNotFound})
		rec := doJSON(r, http.MethodGet, "/v1/integrations/jira/projects", nil, nil, nil)
		require.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("callback service error redirects", func(t *testing.T) {
		r := integrationRouter(sessionID(), fakeConn{completeErr: boom}, fakeFindings{})
		rec := doJSON(r, http.MethodGet, "/v1/integrations/jira/callback?state=s&code=c", nil, nil, nil)
		require.Equal(t, http.StatusFound, rec.Code)
		require.Contains(t, rec.Header().Get("Location"), "connect_error")
	})
}

func TestLogout_NoSessionCookie(t *testing.T) {
	// Authenticated session but no cookie present: logout still clears + 204.
	r := newRouter(RouterDeps{Resolver: fakeResolver{id: sessionID()}, Auth: minimalAuth(), Tokens: minimalTokens()})
	cookies, headers := csrf()
	rec := doJSON(r, http.MethodPost, "/v1/auth/logout", nil, cookies, headers)
	require.Equal(t, http.StatusNoContent, rec.Code)
}
