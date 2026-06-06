// Package oauth holds OAuth provider adapters. JiraOAuthProvider implements the
// Jira Cloud 3LO token lifecycle (authorize URL, code exchange, refresh) on top
// of golang.org/x/oauth2, plus the Jira-specific account resolution. It holds no
// persistence or token-refresh policy.
package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/httpconst"
)

// Jira OAuth protocol constants.
const (
	errCodeInvalidGrant = "invalid_grant"

	jiraAudience            = "api.atlassian.com"
	pathAccessibleResrcs    = "/oauth/token/accessible-resources"
	codeChallengeMethodS256 = "S256"
)

// Jira accessible-resources error messages (the one call x/oauth2 doesn't cover).
const (
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
	ClientID         string
	ClientSecret     string
	RedirectURI      string
	Scopes           []string
	AuthURL          string
	TokenURL         string
	APIBaseURL       string
	UsePKCE          bool
	InactivityWindow time.Duration
	HTTPTimeout      time.Duration
}

// JiraOAuthProvider implements the OAuth provider port for Jira Cloud.
type JiraOAuthProvider struct {
	cfg        JiraConfig
	oauth      *oauth2.Config
	httpClient *http.Client
}

// NewJiraOAuthProvider constructs the provider with an explicit-timeout client.
func NewJiraOAuthProvider(cfg JiraConfig) *JiraOAuthProvider {
	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = httpconst.DefaultClientTimeout
	}
	return &JiraOAuthProvider{
		cfg: cfg,
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			RedirectURL:  cfg.RedirectURI,
			Scopes:       cfg.Scopes,
			// Atlassian expects client credentials in the request body params.
			Endpoint: oauth2.Endpoint{AuthURL: cfg.AuthURL, TokenURL: cfg.TokenURL, AuthStyle: oauth2.AuthStyleInParams},
		},
		httpClient: &http.Client{Timeout: timeout},
	}
}

// Name returns the provider key.
func (p *JiraOAuthProvider) Name() string { return domain.ProviderJira }

// Scopes returns the configured OAuth scopes.
func (p *JiraOAuthProvider) Scopes() []string { return p.cfg.Scopes }

// InactivityWindow is how long a refresh token survives without use.
func (p *JiraOAuthProvider) InactivityWindow() time.Duration { return p.cfg.InactivityWindow }

// BuildAuthorizationURL builds the 3LO consent URL. codeChallenge is included
// only when PKCE is enabled.
func (p *JiraOAuthProvider) BuildAuthorizationURL(state, codeChallenge string) string {
	opts := []oauth2.AuthCodeOption{
		oauth2.SetAuthURLParam("audience", jiraAudience),
		oauth2.SetAuthURLParam("prompt", "consent"),
	}
	if p.cfg.UsePKCE && codeChallenge != "" {
		opts = append(opts,
			oauth2.SetAuthURLParam("code_challenge", codeChallenge),
			oauth2.SetAuthURLParam("code_challenge_method", codeChallengeMethodS256),
		)
	}
	return p.oauth.AuthCodeURL(state, opts...)
}

// ExchangeCodeForTokens swaps an authorization code for tokens and resolves the
// target Jira account (cloudid + site URL).
func (p *JiraOAuthProvider) ExchangeCodeForTokens(ctx context.Context, code, codeVerifier string) (*domain.TokenSet, domain.ProviderAccount, error) {
	var opts []oauth2.AuthCodeOption
	if p.cfg.UsePKCE && codeVerifier != "" {
		opts = append(opts, oauth2.VerifierOption(codeVerifier))
	}
	tok, err := p.oauth.Exchange(p.clientCtx(ctx), code, opts...)
	if err != nil {
		return nil, domain.ProviderAccount{}, mapTokenError(err)
	}

	account, err := p.resolveAccount(ctx, tok.AccessToken)
	if err != nil {
		return nil, domain.ProviderAccount{}, err
	}
	return tokenSetFrom(tok), account, nil
}

// RefreshTokens exchanges a refresh token for a new token set. A rejected refresh
// token surfaces as domain.ErrInvalidGrant.
func (p *JiraOAuthProvider) RefreshTokens(ctx context.Context, refreshToken string) (*domain.TokenSet, error) {
	tok, err := p.oauth.TokenSource(p.clientCtx(ctx), &oauth2.Token{RefreshToken: refreshToken}).Token()
	if err != nil {
		return nil, mapTokenError(err)
	}
	return tokenSetFrom(tok), nil
}

// clientCtx hands x/oauth2 our timeout-bound HTTP client for token requests.
func (p *JiraOAuthProvider) clientCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, oauth2.HTTPClient, p.httpClient)
}

// mapTokenError maps an x/oauth2 rejected-grant error to domain.ErrInvalidGrant
// (which drives the connection to needs_reauth) and wraps everything else.
// ErrorCode is populated by x/oauth2 only for JSON error bodies, so we also parse
// the raw body as a fallback for providers that omit the content-type header.
func mapTokenError(err error) error {
	if isInvalidGrantError(err) {
		return domain.ErrInvalidGrant
	}
	return fmt.Errorf("token request: %w", err)
}

// isInvalidGrantError reports whether err is the provider rejecting the grant
// (the refresh token / auth code is no longer valid), which must drive the
// connection to needs_reauth rather than be treated as a transient failure.
func isInvalidGrantError(err error) bool {
	var re *oauth2.RetrieveError
	return errors.As(err, &re) && (re.ErrorCode == errCodeInvalidGrant || bodyHasInvalidGrant(re.Body))
}

func bodyHasInvalidGrant(body []byte) bool {
	var e struct {
		Error string `json:"error"`
	}
	return json.Unmarshal(body, &e) == nil && e.Error == errCodeInvalidGrant
}

func tokenSetFrom(tok *oauth2.Token) *domain.TokenSet {
	scope, _ := tok.Extra("scope").(string)
	return &domain.TokenSet{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    tok.Expiry,
		Scopes:       strings.Fields(scope),
	}
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

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return domain.ProviderAccount{}, fmt.Errorf(errFmtResourcesRequest, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if !httpconst.IsSuccessStatus(resp.StatusCode) {
		return domain.ProviderAccount{}, fmt.Errorf(errFmtResourcesStatus, resp.StatusCode)
	}
	var resources []accessibleResource
	if err := json.NewDecoder(resp.Body).Decode(&resources); err != nil {
		return domain.ProviderAccount{}, fmt.Errorf(errFmtDecodeResources, err)
	}
	if len(resources) == 0 {
		return domain.ProviderAccount{}, errNoAccessibleSites
	}
	resource := resources[0]
	return domain.ProviderAccount{ID: resource.ID, SiteURL: resource.URL, Name: resource.Name}, nil
}
