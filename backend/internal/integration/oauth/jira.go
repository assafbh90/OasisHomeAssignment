// Package oauth holds OAuth provider adapters. JiraOAuthProvider implements the
// Jira Cloud 3LO token lifecycle (authorize URL, code exchange, refresh) and
// account resolution. It holds no persistence or token-refresh policy.
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/httpconst"
)

// Jira OAuth protocol constants.
const (
	grantAuthorizationCode = "authorization_code"
	grantRefreshToken      = "refresh_token"
	errCodeInvalidGrant    = "invalid_grant"

	jiraAudience            = "api.atlassian.com"
	pathAccessibleResrcs    = "/oauth/token/accessible-resources"
	codeChallengeMethodS256 = "S256"
)

// Jira OAuth error messages, grouped so every failure string for this provider
// lives in one place. The errFmt* values are fmt.Errorf format strings (they
// carry %w/%d verbs); errNoAccessibleSites is a sentinel.
const (
	errFmtBuildTokenRequest     = "build token request: %w"
	errFmtTokenRequest          = "token request: %w"
	errFmtTokenEndpointStatusEr = "token endpoint returned %d (%s)"
	errFmtTokenEndpointStatus   = "token endpoint returned %d"
	errFmtDecodeTokenResponse   = "decode token response: %w"

	errFmtBuildResourcesRequest = "build resources request: %w"
	errFmtResourcesRequest      = "resources request: %w"
	errFmtResourcesStatus       = "accessible-resources returned %d"
	errFmtDecodeResources       = "decode resources: %w"
)

// errNoAccessibleSites means the token grants access to no Jira site.
var errNoAccessibleSites = errors.New("no accessible Jira sites for this account")

// JiraConfig configures the Jira provider. The composition root maps the app
// config into this; tests override the URLs to point at httptest servers.
type JiraConfig struct {
	ClientID            string
	ClientSecret        string
	RedirectURI         string
	Scopes              []string
	AuthURL             string
	TokenURL            string
	APIBaseURL          string
	UsePKCE             bool
	RotatesRefreshToken bool
	InactivityWindow    time.Duration
	HTTPTimeout         time.Duration
}

// JiraOAuthProvider implements the OAuth provider port for Jira Cloud.
type JiraOAuthProvider struct {
	cfg    JiraConfig
	client *http.Client
}

// NewJiraOAuthProvider constructs the provider with an explicit-timeout client.
func NewJiraOAuthProvider(cfg JiraConfig) *JiraOAuthProvider {
	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = httpconst.DefaultClientTimeout
	}
	return &JiraOAuthProvider{cfg: cfg, client: &http.Client{Timeout: timeout}}
}

// Name returns the provider key.
func (p *JiraOAuthProvider) Name() string { return domain.ProviderJira }

// Scopes returns the configured OAuth scopes.
func (p *JiraOAuthProvider) Scopes() []string { return p.cfg.Scopes }

// RotatesRefreshToken reports whether refresh rotates the refresh token (Jira: yes).
func (p *JiraOAuthProvider) RotatesRefreshToken() bool { return p.cfg.RotatesRefreshToken }

// InactivityWindow is how long a refresh token survives without use.
func (p *JiraOAuthProvider) InactivityWindow() time.Duration { return p.cfg.InactivityWindow }

// BuildAuthorizationURL builds the 3LO consent URL. codeChallenge is included
// only when PKCE is enabled.
func (p *JiraOAuthProvider) BuildAuthorizationURL(state, codeChallenge string) string {
	q := url.Values{}
	q.Set("audience", jiraAudience)
	q.Set("client_id", p.cfg.ClientID)
	q.Set("scope", strings.Join(p.cfg.Scopes, " "))
	q.Set("redirect_uri", p.cfg.RedirectURI)
	q.Set("state", state)
	q.Set("response_type", "code")
	q.Set("prompt", "consent")
	if p.cfg.UsePKCE && codeChallenge != "" {
		q.Set("code_challenge", codeChallenge)
		q.Set("code_challenge_method", codeChallengeMethodS256)
	}
	return p.cfg.AuthURL + "?" + q.Encode()
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

// ExchangeCodeForTokens swaps an authorization code for tokens and resolves the
// target Jira account (cloudid + site URL).
func (p *JiraOAuthProvider) ExchangeCodeForTokens(ctx context.Context, code, codeVerifier string) (*domain.TokenSet, domain.ProviderAccount, error) {
	body := map[string]string{
		"grant_type":    grantAuthorizationCode,
		"client_id":     p.cfg.ClientID,
		"client_secret": p.cfg.ClientSecret,
		"code":          code,
		"redirect_uri":  p.cfg.RedirectURI,
	}
	if p.cfg.UsePKCE && codeVerifier != "" {
		body["code_verifier"] = codeVerifier
	}
	tr, err := p.postToken(ctx, body)
	if err != nil {
		return nil, domain.ProviderAccount{}, err
	}
	ts := tokenSetFrom(tr)

	account, err := p.resolveAccount(ctx, ts.AccessToken)
	if err != nil {
		return nil, domain.ProviderAccount{}, err
	}
	return ts, account, nil
}

// RefreshTokens exchanges a refresh token for a new token set. A rejected refresh
// token surfaces as domain.ErrInvalidGrant.
func (p *JiraOAuthProvider) RefreshTokens(ctx context.Context, refreshToken string) (*domain.TokenSet, error) {
	tr, err := p.postToken(ctx, map[string]string{
		"grant_type":    grantRefreshToken,
		"client_id":     p.cfg.ClientID,
		"client_secret": p.cfg.ClientSecret,
		"refresh_token": refreshToken,
	})
	if err != nil {
		return nil, err
	}
	return tokenSetFrom(tr), nil
}

func tokenSetFrom(tr *tokenResponse) *domain.TokenSet {
	return &domain.TokenSet{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		ExpiresAt:    time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
		Scopes:       strings.Fields(tr.Scope),
	}
}

func (p *JiraOAuthProvider) postToken(ctx context.Context, body map[string]string) (*tokenResponse, error) {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.TokenURL, strings.NewReader(string(raw)))
	if err != nil {
		return nil, fmt.Errorf(errFmtBuildTokenRequest, err)
	}
	req.Header.Set(httpconst.HeaderContentType, httpconst.ContentTypeJSON)
	req.Header.Set(httpconst.HeaderAccept, httpconst.ContentTypeJSON)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf(errFmtTokenRequest, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		var er struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&er)
		if er.Error == errCodeInvalidGrant {
			return nil, domain.ErrInvalidGrant
		}
		return nil, fmt.Errorf(errFmtTokenEndpointStatusEr, resp.StatusCode, er.Error)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf(errFmtTokenEndpointStatus, resp.StatusCode)
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return nil, fmt.Errorf(errFmtDecodeTokenResponse, err)
	}
	return &tr, nil
}

type accessibleResource struct {
	ID   string `json:"id"`
	URL  string `json:"url"`
	Name string `json:"name"`
}

// resolveAccount calls the accessible-resources endpoint to map the access token
// to a Jira cloudid + site URL. The first resource is used (single-site PoC).
func (p *JiraOAuthProvider) resolveAccount(ctx context.Context, accessToken string) (domain.ProviderAccount, error) {
	endpoint := strings.TrimRight(p.cfg.APIBaseURL, "/") + pathAccessibleResrcs
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return domain.ProviderAccount{}, fmt.Errorf(errFmtBuildResourcesRequest, err)
	}
	req.Header.Set(httpconst.HeaderAuthorization, httpconst.BearerPrefix+accessToken)
	req.Header.Set(httpconst.HeaderAccept, httpconst.ContentTypeJSON)

	resp, err := p.client.Do(req)
	if err != nil {
		return domain.ProviderAccount{}, fmt.Errorf(errFmtResourcesRequest, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return domain.ProviderAccount{}, fmt.Errorf(errFmtResourcesStatus, resp.StatusCode)
	}
	var resources []accessibleResource
	if err := json.NewDecoder(resp.Body).Decode(&resources); err != nil {
		return domain.ProviderAccount{}, fmt.Errorf(errFmtDecodeResources, err)
	}
	if len(resources) == 0 {
		return domain.ProviderAccount{}, errNoAccessibleSites
	}
	r := resources[0]
	return domain.ProviderAccount{ID: r.ID, SiteURL: r.URL, Name: r.Name}, nil
}
