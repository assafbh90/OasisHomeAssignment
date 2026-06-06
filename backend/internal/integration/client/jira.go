// Package client holds provider API clients — the objects that perform
// operations against an integration's REST API. JiraClient does Jira issue and
// project operations given a ready domain.ClientAuth; it holds no OAuth or
// token-refresh logic (that lives in integration/oauth and integration/oauthtoken).
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/httpconst"
)

// Jira REST API (v3) constants.
const (
	// defaultIssueType is the Jira issue type used for NHI findings.
	defaultIssueType = "Task"

	// pathAPIPrefix is the per-tenant API prefix: <apiBaseURL>/ex/jira/<cloudid>.
	pathAPIPrefix     = "/ex/jira/"
	pathCreateIssue   = "/rest/api/3/issue"
	pathProjectSearch = "/rest/api/3/project/search?maxResults=100"
	pathMyself        = "/rest/api/3/myself"
	browsePath        = "/browse/"

	// errSnippetLimit caps how much of an error response body we include in messages.
	errSnippetLimit = 512
)

// Jira client error messages, grouped so every failure string for this adapter
// lives in one place. All carry fmt.Errorf verbs.
const (
	errFmtMarshalRequest = "marshal request: %w"
	errFmtBuildRequest   = "build jira request: %w"
	errFmtRequest        = "jira request: %w"
	errFmtAPIStatus      = "jira API %s %s returned %d: %s"
	errFmtDecodeResponse = "decode jira response: %w"
)

// JiraClient talks to the Jira Cloud REST API (v3). It builds the per-tenant API
// base URL (<apiBaseURL>/ex/jira/<cloudid>) from the cloudid in each call.
type JiraClient struct {
	apiBaseURL string
	http       *http.Client
}

// NewJiraClient constructs the client. apiBaseURL is the Jira API host
// (e.g. https://api.atlassian.com); the per-tenant path is derived per call.
func NewJiraClient(apiBaseURL string, httpTimeout time.Duration) *JiraClient {
	if httpTimeout <= 0 {
		httpTimeout = httpconst.DefaultClientTimeout
	}
	return &JiraClient{apiBaseURL: apiBaseURL, http: &http.Client{Timeout: httpTimeout}}
}

// Name returns the provider key.
func (c *JiraClient) Name() string { return domain.ProviderJira }

// CreateIssue creates a Jira issue from the generic payload and returns a ref
// with a clickable browse URL.
func (c *JiraClient) CreateIssue(ctx context.Context, auth domain.ClientAuth, payload domain.TicketPayload) (domain.TicketRef, error) {
	fields := map[string]any{
		"project":   map[string]string{"key": payload.ProjectKey},
		"summary":   payload.Title,
		"issuetype": map[string]string{"name": defaultIssueType},
	}
	if desc := strings.TrimSpace(payload.Description); desc != "" {
		fields["description"] = adfDoc(desc)
	}
	if labels := sanitizeLabels(payload.Labels); len(labels) > 0 {
		fields["labels"] = labels
	}

	var out struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := c.doJSON(ctx, http.MethodPost, auth, pathCreateIssue,
		map[string]any{"fields": fields}, &out); err != nil {
		return domain.TicketRef{}, err
	}

	return domain.TicketRef{
		Provider: domain.ProviderJira,
		IssueKey: out.Key,
		URL:      issueURL(auth.SiteURL, out.Key),
	}, nil
}

// ListProjects returns the projects visible to the connected account.
func (c *JiraClient) ListProjects(ctx context.Context, auth domain.ClientAuth) ([]domain.ProjectRef, error) {
	var out struct {
		Values []struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"values"`
	}
	if err := c.doJSON(ctx, http.MethodGet, auth, pathProjectSearch, nil, &out); err != nil {
		return nil, err
	}
	projects := make([]domain.ProjectRef, 0, len(out.Values))
	for _, v := range out.Values {
		projects = append(projects, domain.ProjectRef{Key: v.Key, Name: v.Name})
	}
	return projects, nil
}

// VerifyConnection checks the token works by calling the current-user endpoint.
func (c *JiraClient) VerifyConnection(ctx context.Context, auth domain.ClientAuth) error {
	return c.doJSON(ctx, http.MethodGet, auth, pathMyself, nil, nil)
}

// apiBase returns the per-tenant Jira API base for a cloudid.
func (c *JiraClient) apiBase(cloudID string) string {
	return strings.TrimRight(c.apiBaseURL, "/") + pathAPIPrefix + cloudID
}

// doJSON performs an authenticated JSON request against the per-tenant Jira API
// base and decodes a 2xx response into out (which may be nil to ignore the body).
func (c *JiraClient) doJSON(ctx context.Context, method string, auth domain.ClientAuth, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf(errFmtMarshalRequest, err)
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.apiBase(auth.ExternalAccountID)+path, reader)
	if err != nil {
		return fmt.Errorf(errFmtBuildRequest, err)
	}
	req.Header.Set(httpconst.HeaderAuthorization, httpconst.BearerPrefix+auth.AccessToken)
	req.Header.Set(httpconst.HeaderAccept, httpconst.ContentTypeJSON)
	if body != nil {
		req.Header.Set(httpconst.HeaderContentType, httpconst.ContentTypeJSON)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf(errFmtRequest, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, errSnippetLimit))
		return fmt.Errorf(errFmtAPIStatus, method, path, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf(errFmtDecodeResponse, err)
		}
	}
	return nil
}

// adfDoc wraps plain text in a minimal Atlassian Document Format document, which
// the Jira v3 issue API requires for the description field.
func adfDoc(text string) map[string]any {
	return map[string]any{
		"type":    "doc",
		"version": 1,
		"content": []any{
			map[string]any{
				"type":    "paragraph",
				"content": []any{map[string]any{"type": "text", "text": text}},
			},
		},
	}
}

// sanitizeLabels drops empties and replaces spaces (Jira labels can't contain
// whitespace).
func sanitizeLabels(in []string) []string {
	out := make([]string, 0, len(in))
	for _, l := range in {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		out = append(out, strings.ReplaceAll(l, " ", "-"))
	}
	return out
}

func issueURL(siteURL, key string) string {
	if siteURL == "" || key == "" {
		return ""
	}
	return strings.TrimRight(siteURL, "/") + browsePath + key
}
