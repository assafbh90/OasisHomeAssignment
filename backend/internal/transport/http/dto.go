package http

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/assafbh/identityhub/internal/domain"
)

const (
	rfc3339 = time.RFC3339

	// maxRequestBodyBytes caps decoded request bodies (defense against oversized input).
	maxRequestBodyBytes = 1 << 20 // 1 MiB
	// maxTitleLen bounds the ticket title length.
	maxTitleLen = 255
)

// bindJSON decodes the request body into dst, rejecting unknown fields.
func bindJSON(c *gin.Context, dst any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBodyBytes))
	decoder.DisallowUnknownFields()
	return decoder.Decode(dst)
}

// ---- auth ----

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (r loginRequest) validate() string {
	switch {
	case strings.TrimSpace(r.Email) == "":
		return "email is required"
	case r.Password == "":
		return "password is required"
	default:
		return ""
	}
}

type identityResponse struct {
	UserID     string   `json:"user_id"`
	TenantID   string   `json:"tenant_id"`
	AuthMethod string   `json:"auth_method"`
	Scopes     []string `json:"scopes"`
}

func toIdentityResponse(id domain.Identity) identityResponse {
	scopes := id.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	return identityResponse{
		UserID:     id.UserID.String(),
		TenantID:   id.TenantID.String(),
		AuthMethod: string(id.AuthMethod),
		Scopes:     scopes,
	}
}

// ---- tokens ----

type issueTokenRequest struct {
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	ExpiresAt *time.Time `json:"expires_at"`
}

func (r issueTokenRequest) validate() string {
	if strings.TrimSpace(r.Name) == "" {
		return "name is required"
	}
	for _, scope := range r.Scopes {
		if scope != domain.ScopeIntegrationsRead && scope != domain.ScopeIntegrationsWrite {
			return "unknown scope: " + scope
		}
	}
	if r.ExpiresAt != nil && r.ExpiresAt.Before(time.Now()) {
		return "expires_at must be in the future"
	}
	return ""
}

type issuedTokenResponse struct {
	ID        string   `json:"id"`
	Token     string   `json:"token"` // plaintext, shown once
	Name      string   `json:"name"`
	Prefix    string   `json:"prefix"`
	Scopes    []string `json:"scopes"`
	ExpiresAt *string  `json:"expires_at,omitempty"`
	CreatedAt string   `json:"created_at"`
}

type tokenMetaResponse struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Prefix     string   `json:"prefix"`
	Scopes     []string `json:"scopes"`
	ExpiresAt  *string  `json:"expires_at,omitempty"`
	LastUsedAt *string  `json:"last_used_at,omitempty"`
	RevokedAt  *string  `json:"revoked_at,omitempty"`
	CreatedAt  string   `json:"created_at"`
}

func toTokenMetaResponse(m domain.TokenMeta) tokenMetaResponse {
	return tokenMetaResponse{
		ID:         m.ID.String(),
		Name:       m.Name,
		Prefix:     m.Prefix,
		Scopes:     emptyIfNil(m.Scopes),
		ExpiresAt:  timePtr(m.ExpiresAt),
		LastUsedAt: timePtr(m.LastUsedAt),
		RevokedAt:  timePtr(m.RevokedAt),
		CreatedAt:  m.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// ---- integrations ----

type ticketRequest struct {
	ProjectKey  string   `json:"project_key"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Labels      []string `json:"labels"`
}

func (r ticketRequest) validate() string {
	switch {
	case strings.TrimSpace(r.ProjectKey) == "":
		return "project_key is required"
	case strings.TrimSpace(r.Title) == "":
		return "title is required"
	case len(r.Title) > maxTitleLen:
		return "title must be at most 255 characters"
	default:
		return ""
	}
}

type ticketResponse struct {
	Provider string `json:"provider"`
	IssueKey string `json:"issue_key"`
	URL      string `json:"url"`
}

type projectResponse struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

type connectionResponse struct {
	Provider  string   `json:"provider"`
	Status    string   `json:"status"`
	Connected bool     `json:"connected"`
	Scopes    []string `json:"scopes"`
}

type recentTicketResponse struct {
	IssueKey   string `json:"issue_key"`
	Title      string `json:"title"`
	URL        string `json:"url"`
	ProjectKey string `json:"project_key"`
	CreatedAt  string `json:"created_at"`
}

type authURLResponse struct {
	AuthURL string `json:"auth_url"`
}

func emptyIfNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func timePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}

// ---- automations ----

type automationRequest struct {
	Name            string `json:"name"`
	SiteURL         string `json:"site_url"`
	ProjectKey      string `json:"project_key"`
	IntervalSeconds int    `json:"interval_seconds"`
	Enabled         *bool  `json:"enabled"`
}

const minIntervalSeconds = 60

func (r automationRequest) validate() string {
	switch {
	case strings.TrimSpace(r.Name) == "":
		return "name is required"
	case !strings.HasPrefix(r.SiteURL, "http://") && !strings.HasPrefix(r.SiteURL, "https://"):
		return "site_url must be an http(s) URL"
	case strings.TrimSpace(r.ProjectKey) == "":
		return "project_key is required"
	case r.IntervalSeconds != 0 && r.IntervalSeconds < minIntervalSeconds:
		return "interval_seconds must be at least 60"
	default:
		return ""
	}
}

type automationResponse struct {
	ID              string  `json:"id"`
	Name            string  `json:"name"`
	SiteURL         string  `json:"site_url"`
	Provider        string  `json:"provider"`
	ProjectKey      string  `json:"project_key"`
	IntervalSeconds int     `json:"interval_seconds"`
	Enabled         bool    `json:"enabled"`
	Status          string  `json:"status"`
	NextScanAt      string  `json:"next_scan_at"`
	LastRunAt       *string `json:"last_run_at,omitempty"`
	LastError       string  `json:"last_error,omitempty"`
	CreatedAt       string  `json:"created_at"`
}

func toAutomationResponse(a domain.Automation) automationResponse {
	var lastRun *string
	if a.LastRunAt != nil {
		s := a.LastRunAt.UTC().Format(rfc3339)
		lastRun = &s
	}
	return automationResponse{
		ID:              a.ID.String(),
		Name:            a.Name,
		SiteURL:         a.SiteURL,
		Provider:        a.Provider,
		ProjectKey:      a.ProjectKey,
		IntervalSeconds: int(a.Interval.Seconds()),
		Enabled:         a.Enabled,
		Status:          string(a.Status),
		NextScanAt:      a.NextScanAt.UTC().Format(rfc3339),
		LastRunAt:       lastRun,
		LastError:       a.LastError,
		CreatedAt:       a.CreatedAt.UTC().Format(rfc3339),
	}
}
