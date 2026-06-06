//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/apitoken"
	"github.com/assafbh/identityhub/internal/auth"
	"github.com/assafbh/identityhub/internal/config"
	"github.com/assafbh/identityhub/internal/integration"
	jiraclient "github.com/assafbh/identityhub/internal/integration/client"
	"github.com/assafbh/identityhub/internal/integration/oauth"
	"github.com/assafbh/identityhub/internal/integration/oauthtoken"
	"github.com/assafbh/identityhub/internal/logging"
	"github.com/assafbh/identityhub/internal/platform"
	"github.com/assafbh/identityhub/internal/session"
	store "github.com/assafbh/identityhub/internal/storage/postgres"
	redisstore "github.com/assafbh/identityhub/internal/storage/redis"
	"github.com/assafbh/identityhub/internal/ticketreport"
	transport "github.com/assafbh/identityhub/internal/transport/http"
)

// --- mock Jira ---

type mockJira struct {
	server          *httptest.Server
	invalidGrant    bool
	accessToken     string
	createdIssueKey string

	mu      sync.Mutex
	created []string // issue keys "on the site", returned by the label search
}

// seedIssue pre-populates an issue as if it already existed on the site (e.g.
// created by another user before a fresh start).
func (m *mockJira) seedIssue(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.created = append(m.created, key)
}

func newMockJira() *mockJira {
	m := &mockJira{accessToken: "at-1", createdIssueKey: "NHI-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		// x/oauth2 sends form-encoded requests and expects JSON token responses.
		w.Header().Set("Content-Type", "application/json")
		if r.PostFormValue("grant_type") == "refresh_token" && m.invalidGrant {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": m.accessToken, "refresh_token": "rt-1", "expires_in": 3600,
			"scope": "read:jira-work write:jira-work offline_access",
		})
	})
	mux.HandleFunc("/oauth/token/accessible-resources", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"id": "cloud-1", "url": "https://acme.atlassian.net", "name": "acme"},
		})
	})
	mux.HandleFunc("/ex/jira/cloud-1/rest/api/3/issue", func(w http.ResponseWriter, _ *http.Request) {
		m.seedIssue(m.createdIssueKey)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "10001", "key": m.createdIssueKey})
	})
	mux.HandleFunc("/ex/jira/cloud-1/rest/api/3/project/search", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"values": []map[string]string{{"key": "NHI", "name": "NHI Findings"}}})
	})
	mux.HandleFunc("/ex/jira/cloud-1/rest/api/3/search/jql", func(w http.ResponseWriter, _ *http.Request) {
		m.mu.Lock()
		keys := append([]string(nil), m.created...)
		m.mu.Unlock()
		issues := make([]map[string]any, 0, len(keys))
		for _, k := range keys {
			issues = append(issues, map[string]any{
				"key": k,
				"fields": map[string]any{
					"summary": "NHI finding", "created": "2026-06-06T12:00:00.000+0000",
					"project": map[string]string{"key": "NHI"},
				},
			})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"issues": issues, "isLast": true})
	})
	m.server = httptest.NewServer(mux)
	return m
}

// --- test environment ---

type env struct {
	handler    http.Handler
	tenantRepo *store.PostgresTenantRepository
	userRepo   *store.PostgresUserRepository
	credRepo   *store.PostgresCredentialRepository
	hasher     *auth.Argon2PasswordHasher
	jira       *mockJira
}

func newEnv(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	// Clean slate per test.
	_, err := adminPool.Exec(ctx, `TRUNCATE tenants, users, api_tokens, integration_credentials RESTART IDENTITY CASCADE`)
	require.NoError(t, err)
	require.NoError(t, redisClient.FlushDB(ctx).Err())

	jira := newMockJira()
	t.Cleanup(jira.server.Close)

	key, _ := config.CryptoConfig{TokenKey: cryptoKeyB64}.DecodedTokenKey()
	cipher, err := oauthtoken.NewAESGCMTokenCipher(key)
	require.NoError(t, err)

	tenantRepo := store.NewPostgresTenantRepository(appPool)
	userRepo := store.NewPostgresUserRepository(appPool)
	tokenRepo := store.NewPostgresApiTokenRepository(appPool)
	credRepo := store.NewPostgresCredentialRepository(appPool, cipher)

	sessionStore := redisstore.NewRedisSessionStore(redisClient)
	tokenCache := redisstore.NewRedisTokenCache(redisClient)
	rateLimiter := redisstore.NewRedisRateLimiter(redisClient, 100, time.Minute)
	stateStore := redisstore.NewRedisOAuthStateStore(redisClient, 10*time.Minute)
	ticketCache := redisstore.NewRedisTicketCache(redisClient, time.Hour)
	reconcileGate := redisstore.NewRedisReconcileGate(redisClient, time.Minute)

	hasher := auth.NewArgon2PasswordHasher(auth.Argon2Params{Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32})
	authenticator := auth.NewUserAuthenticator(userRepo, hasher)
	sessionMgr := session.NewManager(sessionStore, time.Hour, 24*time.Hour)
	tokenIssuer := apitoken.NewTokenIssuer(tokenRepo, tokenCache, "ih_pat", time.Minute)

	jiraProvider := oauth.NewJiraOAuthProvider(oauth.JiraConfig{
		ClientID: "cid", ClientSecret: "csecret", RedirectURI: "http://localhost/v1/integrations/jira/callback",
		Scopes:   []string{"read:jira-work", "write:jira-work", "offline_access"},
		AuthURL:  "https://auth.atlassian.com/authorize",
		TokenURL: jira.server.URL + "/oauth/token", APIBaseURL: jira.server.URL,
		UsePKCE: true, InactivityWindow: 2160 * time.Hour, HTTPTimeout: 5 * time.Second,
	})
	jiraClient := jiraclient.NewJiraClient(jira.server.URL, 5*time.Second)
	tokenManager := oauthtoken.NewReactiveTokenManager(oauthtoken.Deps{
		Repo: credRepo, Provider: jiraProvider, ProviderName: jiraProvider.Name(), Skew: time.Minute,
	})
	connectionSvc := integration.NewConnectionService(integration.Deps{
		Provider: jiraProvider, State: stateStore, Repo: credRepo, UsePKCE: true,
	})
	reportSvc := ticketreport.NewService(ticketreport.Deps{
		Provider: jiraProvider.Name(), Tokens: tokenManager, Creds: credRepo,
		Client: jiraClient, Cache: ticketCache, Gate: reconcileGate,
	})

	resolver := transport.NewChainIdentityResolver(
		transport.NewBearerTokenResolver(tokenIssuer),
		transport.NewSessionIdentityResolver(sessionMgr, "ih_session"),
	)
	cookie := transport.CookieConfig{SessionName: "ih_session", Secure: false, MaxAge: time.Hour}
	handler := transport.NewRouter(transport.RouterDeps{
		Logger:      logging.New("dev", "error"),
		Resolver:    resolver,
		Auth:        transport.NewAuthHandler(authenticator, sessionMgr, rateLimiter, cookie),
		Tokens:      transport.NewTokenHandler(tokenIssuer),
		Health:      transport.NewHealthHandler(appPool, platform.RedisPinger{Client: redisClient}),
		Integration: transport.NewIntegrationHandler(connectionSvc, reportSvc, "/"),
	})

	return &env{handler: handler, tenantRepo: tenantRepo, userRepo: userRepo, credRepo: credRepo, hasher: hasher, jira: jira}
}

// seedUser creates a tenant + user and returns the org slug, email, password.
func (e *env) seedUser(t *testing.T) (org, email, password string, tenantID, userID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	org, email, password = "acme", "admin@acme.test", "password123"
	tenant, err := e.tenantRepo.Create(ctx, org, "Acme")
	require.NoError(t, err)
	hash, err := e.hasher.HashPassword(ctx, password)
	require.NoError(t, err)
	user, err := e.userRepo.CreateUser(ctx, tenant.ID, email, hash)
	require.NoError(t, err)
	return org, email, password, tenant.ID, user.ID
}

// --- HTTP client with cookie jar + CSRF ---

type client struct {
	t    *testing.T
	hc   *http.Client
	base string
}

func newClient(t *testing.T, srv *httptest.Server) *client {
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &client{t: t, hc: hc, base: srv.URL}
}

// response wraps *http.Response with a terse Code() accessor for tests.
type response struct{ *http.Response }

func (r response) Code() int { return r.StatusCode }

func (c *client) do(method, path, bearer string, body any) response {
	c.t.Helper()
	var r *strings.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		r = strings.NewReader(string(raw))
	} else {
		r = strings.NewReader("")
	}
	req, err := http.NewRequest(method, c.base+path, r)
	require.NoError(c.t, err)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if method != http.MethodGet {
		if csrf := c.csrf(); csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
	}
	resp, err := c.hc.Do(req)
	require.NoError(c.t, err)
	return response{resp}
}

func (c *client) csrf() string {
	u, _ := url.Parse(c.base)
	for _, ck := range c.hc.Jar.Cookies(u) {
		if ck.Name == "ih_csrf" {
			return ck.Value
		}
	}
	return ""
}

func decode[T any](t *testing.T, resp response) T {
	t.Helper()
	defer func() { _ = resp.Body.Close() }()
	var out T
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&out))
	return out
}
