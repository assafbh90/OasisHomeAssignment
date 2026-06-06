package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/domain"
)

type failPinger struct{}

func (failPinger) Ping(context.Context) error { return errors.New("down") }

func TestHealthHandler(t *testing.T) {
	t.Run("live", func(t *testing.T) {
		r := newRouter(RouterDeps{Resolver: fakeResolver{}, Auth: minimalAuth(), Tokens: minimalTokens()})
		require.Equal(t, http.StatusOK, doJSON(r, http.MethodGet, "/healthz", nil, nil, nil).Code)
	})

	t.Run("ready ok", func(t *testing.T) {
		r := newRouter(RouterDeps{
			Resolver: fakeResolver{}, Auth: minimalAuth(), Tokens: minimalTokens(),
			Health: NewHealthHandler(okPinger{}, okPinger{}),
		})
		require.Equal(t, http.StatusOK, doJSON(r, http.MethodGet, "/readyz", nil, nil, nil).Code)
	})

	t.Run("ready unhealthy", func(t *testing.T) {
		r := newRouter(RouterDeps{
			Resolver: fakeResolver{}, Auth: minimalAuth(), Tokens: minimalTokens(),
			Health: NewHealthHandler(okPinger{}, failPinger{}),
		})
		rec := doJSON(r, http.MethodGet, "/readyz", nil, nil, nil)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	})
}

func TestRespondError_Mapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrInvalidCredentials, http.StatusUnauthorized, errCodeInvalidCredentials},
		{domain.ErrSessionNotFound, http.StatusUnauthorized, errCodeUnauthenticated},
		{domain.ErrTokenExpired, http.StatusUnauthorized, errCodeUnauthenticated},
		{domain.ErrForbiddenScope, http.StatusForbidden, errCodeForbidden},
		{domain.ErrTenantMismatch, http.StatusForbidden, errCodeForbidden},
		{domain.ErrProviderNotSupported, http.StatusNotFound, errCodeProviderNotSupported},
		{domain.ErrCredentialNotFound, http.StatusNotFound, errCodeNotConnected},
		{domain.ErrStateNotFound, http.StatusBadRequest, errCodeInvalidState},
		{errors.New("boom"), http.StatusInternalServerError, errCodeInternal},
	}
	for _, tc := range tests {
		t.Run(tc.code, func(t *testing.T) {
			t.Parallel()
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
			respondError(c, tc.err)
			require.Equal(t, tc.status, w.Code)
			var body errorResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			require.Equal(t, tc.code, body.Error)
		})
	}
}

func TestSecureHeadersAndRequestID(t *testing.T) {
	t.Parallel()
	r := newRouter(RouterDeps{Resolver: fakeResolver{}, Auth: minimalAuth(), Tokens: minimalTokens(), TLSEnabled: true})
	rec := doJSON(r, http.MethodGet, "/healthz", nil, nil, nil)
	require.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	require.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	require.NotEmpty(t, rec.Header().Get("Strict-Transport-Security"))
	require.NotEmpty(t, rec.Header().Get(headerRequestID))
}

func TestCORS(t *testing.T) {
	t.Parallel()
	r := newRouter(RouterDeps{
		Resolver: fakeResolver{}, Auth: minimalAuth(), Tokens: minimalTokens(),
		AllowOrigin: []string{"https://app.example.com"},
	})

	rec := doJSON(r, http.MethodGet, "/healthz", nil, nil, map[string]string{"Origin": "https://app.example.com"})
	require.Equal(t, "https://app.example.com", rec.Header().Get("Access-Control-Allow-Origin"))
	require.Equal(t, "true", rec.Header().Get("Access-Control-Allow-Credentials"))

	// Preflight short-circuits to 204.
	rec = doJSON(r, http.MethodOptions, "/v1/auth/me", nil, nil, map[string]string{"Origin": "https://app.example.com"})
	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestIntegrationHandlers_StatusListDisconnect(t *testing.T) {
	id := sessionID()
	conn := fakeConn{info: domain.ConnectionInfo{Provider: "jira", Status: domain.StatusConnected, Connected: true, Scopes: []string{"read:jira-work"}}}
	r := integrationRouter(id, conn, fakeFindings{})
	cookies, headers := csrf()

	// status
	rec := doJSON(r, http.MethodGet, "/v1/integrations/jira/status", nil, nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"connected":true`)

	// list
	rec = doJSON(r, http.MethodGet, "/v1/integrations", nil, nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "integrations")

	// projects
	rec = doJSON(r, http.MethodGet, "/v1/integrations/jira/projects", nil, nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "NHI")

	// disconnect (write scope; session is full-scope)
	rec = doJSON(r, http.MethodDelete, "/v1/integrations/jira", nil, cookies, headers)
	require.Equal(t, http.StatusNoContent, rec.Code)

	// recent tickets with project
	rec = doJSON(r, http.MethodGet, "/v1/integrations/jira/tickets?project=NHI", nil, nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestCallback_MissingParamsRedirects(t *testing.T) {
	r := integrationRouter(sessionID(), fakeConn{}, fakeFindings{})
	rec := doJSON(r, http.MethodGet, "/v1/integrations/jira/callback", nil, nil, nil)
	require.Equal(t, http.StatusFound, rec.Code) // redirect with connect_error
	require.Contains(t, rec.Header().Get("Location"), "connect_error")
}

func TestTokenList(t *testing.T) {
	r := newRouter(RouterDeps{Resolver: fakeResolver{id: sessionID()}, Auth: minimalAuth(), Tokens: NewTokenHandler(&fakeTokens{})})
	rec := doJSON(r, http.MethodGet, "/v1/tokens", nil, nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "tokens")
}

func minimalAuth() *AuthHandler {
	return NewAuthHandler(fakeAuthn{}, &fakeSession{}, fakeLimiter{allow: true}, testCookie())
}

func minimalTokens() *TokenHandler { return NewTokenHandler(&fakeTokens{}) }
