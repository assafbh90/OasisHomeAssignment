//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/domain"
)

func TestAuthFlow_LoginMeLogout(t *testing.T) {
	e := newEnv(t)
	_, email, password, _, _ := e.seedUser(t)
	srv := httptest.NewServer(e.handler)
	defer srv.Close()
	c := newClient(t, srv)

	// login
	resp := c.do(http.MethodPost, "/v1/auth/login", "", loginBody(email, password))
	require.Equal(t, http.StatusOK, resp.Code())

	// authenticated /me
	resp = c.do(http.MethodGet, "/v1/auth/me", "", nil)
	require.Equal(t, http.StatusOK, resp.Code())

	// logout
	resp = c.do(http.MethodPost, "/v1/auth/logout", "", nil)
	require.Equal(t, http.StatusNoContent, resp.Code())

	// session no longer valid
	resp = c.do(http.MethodGet, "/v1/auth/me", "", nil)
	require.Equal(t, http.StatusUnauthorized, resp.Code())
}

func TestAuthFlow_BadPassword(t *testing.T) {
	e := newEnv(t)
	_, email, _, _, _ := e.seedUser(t)
	srv := httptest.NewServer(e.handler)
	defer srv.Close()
	c := newClient(t, srv)

	resp := c.do(http.MethodPost, "/v1/auth/login", "", loginBody(email, "wrong"))
	require.Equal(t, http.StatusUnauthorized, resp.Code())
}

func TestAPITokenFlow(t *testing.T) {
	e := newEnv(t)
	_, email, password, _, _ := e.seedUser(t)
	srv := httptest.NewServer(e.handler)
	defer srv.Close()
	c := newClient(t, srv)

	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/v1/auth/login", "", loginBody(email, password)).Code())

	// issue a token (session-authenticated)
	resp := c.do(http.MethodPost, "/v1/tokens", "", map[string]any{"name": "ci", "scopes": []string{"integrations:write"}})
	require.Equal(t, http.StatusCreated, resp.Code())
	issued := decode[map[string]any](t, resp)
	plaintext, _ := issued["token"].(string)
	require.NotEmpty(t, plaintext)
	tokenID, _ := issued["id"].(string)

	// use the bearer token (fresh client, no cookies)
	bc := newClient(t, srv)
	resp = bc.do(http.MethodGet, "/v1/auth/me", plaintext, nil)
	require.Equal(t, http.StatusOK, resp.Code())
	me := decode[map[string]any](t, resp)
	require.Equal(t, "machine_token", me["auth_method"])

	// revoke it (session client) and confirm the bearer is rejected
	require.Equal(t, http.StatusNoContent, c.do(http.MethodDelete, "/v1/tokens/"+tokenID, "", nil).Code())
	resp = bc.do(http.MethodGet, "/v1/auth/me", plaintext, nil)
	require.Equal(t, http.StatusUnauthorized, resp.Code())
}

func TestJiraEndToEnd(t *testing.T) {
	e := newEnv(t)
	_, email, password, _, _ := e.seedUser(t)
	srv := httptest.NewServer(e.handler)
	defer srv.Close()
	c := newClient(t, srv)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/v1/auth/login", "", loginBody(email, password)).Code())

	// connect -> get auth URL, extract state
	resp := c.do(http.MethodGet, "/v1/integrations/jira/connect", "", nil)
	require.Equal(t, http.StatusOK, resp.Code())
	authURL := decode[map[string]string](t, resp)["auth_url"]
	state := extractQuery(t, authURL, "state")
	require.NotEmpty(t, state)

	// callback completes the connection (state + identity cross-check)
	resp = c.do(http.MethodGet, "/v1/integrations/jira/callback?state="+url.QueryEscape(state)+"&code=fake-code", "", nil)
	require.Equal(t, http.StatusFound, resp.Code()) // redirect back to SPA

	// status is connected
	resp = c.do(http.MethodGet, "/v1/integrations/jira/status", "", nil)
	require.Equal(t, http.StatusOK, resp.Code())
	require.True(t, decode[map[string]any](t, resp)["connected"].(bool))

	// credential is encrypted at rest (raw bytes != plaintext token)
	assertCredentialEncrypted(t)

	// create a ticket
	resp = c.do(http.MethodPost, "/v1/integrations/jira/tickets", "", map[string]any{"project_key": "NHI", "title": "Stale Service Account"})
	require.Equal(t, http.StatusCreated, resp.Code())
	require.Equal(t, "NHI-1", decode[map[string]string](t, resp)["issue_key"])

	// recent tickets shows it
	resp = c.do(http.MethodGet, "/v1/integrations/jira/tickets?project=NHI", "", nil)
	require.Equal(t, http.StatusOK, resp.Code())
	tickets := decode[map[string][]map[string]any](t, resp)["tickets"]
	require.Len(t, tickets, 1)
	require.Equal(t, "NHI-1", tickets[0]["issue_key"])

	// disconnect, then creating a ticket reports not-connected
	require.Equal(t, http.StatusNoContent, c.do(http.MethodDelete, "/v1/integrations/jira", "", nil).Code())
	resp = c.do(http.MethodPost, "/v1/integrations/jira/tickets", "", map[string]any{"project_key": "NHI", "title": "x"})
	require.Equal(t, http.StatusNotFound, resp.Code())
}

func TestReauthFlow(t *testing.T) {
	e := newEnv(t)
	_, email, password, tenantID, userID := e.seedUser(t)
	srv := httptest.NewServer(e.handler)
	defer srv.Close()
	c := newClient(t, srv)
	require.Equal(t, http.StatusOK, c.do(http.MethodPost, "/v1/auth/login", "", loginBody(email, password)).Code())

	// connect
	authURL := decode[map[string]string](t, c.do(http.MethodGet, "/v1/integrations/jira/connect", "", nil))["auth_url"]
	state := extractQuery(t, authURL, "state")
	require.Equal(t, http.StatusFound, c.do(http.MethodGet, "/v1/integrations/jira/callback?state="+url.QueryEscape(state)+"&code=fake", "", nil).Code())

	// Force the access token expired and make refresh fail with invalid_grant.
	_, err := adminPool.Exec(context.Background(),
		`UPDATE integration_credentials SET access_expires_at = now() - interval '1 hour' WHERE tenant_id=$1 AND user_id=$2`,
		tenantID, userID)
	require.NoError(t, err)
	e.jira.invalidGrant = true

	// Creating a ticket now returns a reauth-required signal (409).
	resp := c.do(http.MethodPost, "/v1/integrations/jira/tickets", "", map[string]any{"project_key": "NHI", "title": "x"})
	require.Equal(t, http.StatusConflict, resp.Code())
	body := decode[map[string]any](t, resp)
	require.Equal(t, "reauth_required", body["error"])

	// And the connection is now marked needs_reauth.
	resp = c.do(http.MethodGet, "/v1/integrations/jira/status", "", nil)
	require.Equal(t, "needs_reauth", decode[map[string]any](t, resp)["status"])
}

func TestTenantIsolation_RLS(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// Two tenants, each with a connected credential.
	tA, _ := e.tenantRepo.Create(ctx, "acme", "Acme")
	hash, _ := e.hasher.HashPassword(ctx, "pw")
	uA, _ := e.userRepo.CreateUser(ctx, tA.ID, "a@acme.test", hash)
	tB, _ := e.tenantRepo.Create(ctx, "globex", "Globex")
	uB, _ := e.userRepo.CreateUser(ctx, tB.ID, "b@globex.test", hash)

	saveCred(t, e, tA.ID, uA.ID, "cloud-a")
	saveCred(t, e, tB.ID, uB.ID, "cloud-b")

	// Repository scoping: tenant A cannot load tenant B's credential.
	_, err := e.credRepo.LoadCredential(ctx, tA.ID, uB.ID, domain.ProviderJira)
	require.ErrorIs(t, err, domain.ErrCredentialNotFound)
	got, err := e.credRepo.LoadCredential(ctx, tA.ID, uA.ID, domain.ProviderJira)
	require.NoError(t, err)
	require.Equal(t, "cloud-a", got.ExternalAccountID)

	// RLS: under tenant A's GUC, only A's row is visible.
	require.Equal(t, 1, countCredsUnderTenant(t, tA.ID))
	require.Equal(t, 1, countCredsUnderTenant(t, tB.ID))
	// With no tenant GUC set, RLS denies everything (default-deny).
	require.Equal(t, 0, countCredsNoTenant(t))
}

// --- helpers ---

func loginBody(email, password string) map[string]string {
	return map[string]string{"email": email, "password": password}
}

func saveCred(t *testing.T, e *env, tenantID, userID uuid.UUID, cloud string) {
	t.Helper()
	err := e.credRepo.SaveCredential(context.Background(), &domain.Credential{
		TenantID: tenantID, UserID: userID, Provider: domain.ProviderJira,
		AccessToken: "at", RefreshToken: "rt", Scopes: []string{"read:jira-work"},
		ExternalAccountID: cloud, SiteURL: "https://x.atlassian.net",
		AccessExpiresAt: time.Now().Add(time.Hour), RefreshLastUsedAt: time.Now(), Status: domain.StatusConnected,
	})
	require.NoError(t, err)
}

func countCredsUnderTenant(t *testing.T, tenantID uuid.UUID) int {
	t.Helper()
	ctx := context.Background()
	tx, err := appPool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID.String())
	require.NoError(t, err)
	var n int
	require.NoError(t, tx.QueryRow(ctx, "SELECT count(*) FROM integration_credentials").Scan(&n))
	return n
}

func countCredsNoTenant(t *testing.T) int {
	t.Helper()
	var n int
	err := appPool.QueryRow(context.Background(), "SELECT count(*) FROM integration_credentials").Scan(&n)
	require.NoError(t, err)
	return n
}

func assertCredentialEncrypted(t *testing.T) {
	t.Helper()
	var raw []byte
	// adminPool bypasses RLS; read the stored ciphertext directly.
	err := adminPool.QueryRow(context.Background(), "SELECT access_token FROM integration_credentials LIMIT 1").Scan(&raw)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "at-1", "token must be encrypted at rest")
	require.Greater(t, len(raw), 12, "ciphertext should include nonce + tag")
}

func extractQuery(t *testing.T, rawURL, key string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	require.NoError(t, err)
	return u.Query().Get(key)
}
