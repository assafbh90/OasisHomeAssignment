package oauth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/integration/oauth"
)

func newProvider(serverURL string) *oauth.JiraOAuthProvider {
	return oauth.NewJiraOAuthProvider(oauth.JiraConfig{
		ClientID:         "client-id",
		ClientSecret:     "client-secret",
		RedirectURI:      "https://app.example.com/callback",
		Scopes:           []string{"read:jira-work", "write:jira-work", "offline_access"},
		AuthURL:          "https://auth.atlassian.com/authorize",
		TokenURL:         serverURL + "/oauth/token",
		APIBaseURL:       serverURL,
		UsePKCE:          true,
		InactivityWindow: 2160 * time.Hour,
	})
}

func TestJiraOAuthProvider_BuildAuthorizationURL(t *testing.T) {
	t.Parallel()
	p := newProvider("https://api.atlassian.com")
	got := p.BuildAuthorizationURL("the-state", "the-challenge")

	require.Contains(t, got, "client_id=client-id")
	require.Contains(t, got, "state=the-state")
	require.Contains(t, got, "code_challenge=the-challenge")
	require.Contains(t, got, "code_challenge_method=S256")
	require.Contains(t, got, "response_type=code")
	require.Contains(t, got, "offline_access")
}

func TestJiraOAuthProvider_ExchangeCodeForTokens(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			// x/oauth2 sends form-encoded token requests.
			// assert (not require) inside an HTTP handler — never call FailNow off
			// the test goroutine.
			assert.Equal(t, "authorization_code", r.PostFormValue("grant_type"))
			assert.Equal(t, "the-code", r.PostFormValue("code"))
			assert.Equal(t, "the-verifier", r.PostFormValue("code_verifier"))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at", "refresh_token": "rt", "expires_in": 3600, "scope": "read:jira-work write:jira-work",
			})
		case "/oauth/token/accessible-resources":
			assert.Equal(t, "Bearer at", r.Header.Get("Authorization"))
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"id": "cloud-123", "url": "https://acme.atlassian.net", "name": "acme"},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	p := newProvider(srv.URL)
	ts, account, err := p.ExchangeCodeForTokens(context.Background(), "the-code", "the-verifier")
	require.NoError(t, err)
	require.Equal(t, "at", ts.AccessToken)
	require.Equal(t, "rt", ts.RefreshToken)
	require.True(t, ts.ExpiresAt.After(time.Now()))
	wantAccount := domain.ProviderAccount{ID: "cloud-123", SiteURL: "https://acme.atlassian.net", Name: "acme"}
	if diff := cmp.Diff(wantAccount, account); diff != "" {
		t.Fatalf("account mismatch (-want +got):\n%s", diff)
	}
}

func TestJiraOAuthProvider_RefreshTokens(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "refresh_token", r.PostFormValue("grant_type"))
			assert.Equal(t, "old-rt", r.PostFormValue("refresh_token"))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "new-at", "refresh_token": "new-rt", "expires_in": 3600, "scope": "read:jira-work",
			})
		}))
		defer srv.Close()

		p := newProvider(srv.URL)
		ts, err := p.RefreshTokens(context.Background(), "old-rt")
		require.NoError(t, err)
		require.Equal(t, "new-at", ts.AccessToken)
		require.Equal(t, "new-rt", ts.RefreshToken)
	})

	t.Run("invalid_grant", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh token is invalid"}`))
		}))
		defer srv.Close()

		p := newProvider(srv.URL)
		_, err := p.RefreshTokens(context.Background(), "dead-rt")
		require.ErrorIs(t, err, domain.ErrInvalidGrant)
	})
}

func TestJiraOAuthProvider_BuildAuthorizationURL_NoPKCE(t *testing.T) {
	t.Parallel()
	p := oauth.NewJiraOAuthProvider(oauth.JiraConfig{
		ClientID: "cid", RedirectURI: "http://x/cb", Scopes: []string{"read:jira-work"},
		AuthURL: "https://auth.atlassian.com/authorize", UsePKCE: false,
	})
	got := p.BuildAuthorizationURL("st", "")
	require.Contains(t, got, "state=st")
	require.NotContains(t, got, "code_challenge")
}

func TestJiraOAuthProvider_TokenEndpoint5xx(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := newProvider(srv.URL)
	_, err := p.RefreshTokens(context.Background(), "rt")
	require.Error(t, err)
	require.NotErrorIs(t, err, domain.ErrInvalidGrant) // transient, not reauth
}

func TestJiraOAuthProvider_NoAccessibleSites(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/oauth/token" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "refresh_token": "rt", "expires_in": 3600})
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]string{}) // empty accessible-resources
	}))
	defer srv.Close()

	p := newProvider(srv.URL)
	_, _, err := p.ExchangeCodeForTokens(context.Background(), "code", "verifier")
	require.Error(t, err)
}
