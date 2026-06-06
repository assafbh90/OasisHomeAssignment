// Package domain holds the core entities, value objects, and domain errors.
// It is pure Go: it imports no transport, storage, or third-party infra.
package domain

import (
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
)

// Sentinel domain errors. Adapters wrap these with %w; transport maps them to
// HTTP status codes. Client-facing messages stay generic (see transport) to
// avoid leaking which factor failed.
var (
	// Auth
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrSessionNotFound    = errors.New("session not found")
	ErrUserNotFound       = errors.New("user not found")
	ErrTenantNotFound     = errors.New("tenant not found")
	ErrTokenNotFound      = errors.New("token not found")
	ErrTokenRevoked       = errors.New("token revoked")
	ErrTokenExpired       = errors.New("token expired")

	// Authorization / tenancy
	ErrTenantMismatch  = errors.New("tenant mismatch")
	ErrForbiddenScope  = errors.New("forbidden scope")
	ErrUnauthenticated = errors.New("unauthenticated")

	// Integration
	ErrReauthRequired       = errors.New("reauth required")
	ErrCredentialNotFound   = errors.New("credential not found")
	ErrProviderNotSupported = errors.New("provider not supported")
	ErrStateNotFound        = errors.New("oauth state not found or already used")

	// Automation
	ErrAutomationNotFound = errors.New("automation not found")

	// ErrInvalidGrant is returned by a provider when a refresh token is no longer
	// valid (e.g. rotated/expired). It drives the connection to needs_reauth.
	ErrInvalidGrant = errors.New("invalid_grant")
)

// --- Identity & auth ---------------------------------------------------------

// AuthMethod identifies how a caller authenticated.
type AuthMethod string

const (
	AuthMethodSession AuthMethod = "session"       // interactive browser session (cookie)
	AuthMethodToken   AuthMethod = "machine_token" // API key (bearer) for bots/CI
)

// Application scopes carried by API keys (and, for sessions, derived from the
// user's role). Integration write endpoints require ScopeIntegrationsWrite.
const (
	ScopeIntegrationsRead  = "integrations:read"
	ScopeIntegrationsWrite = "integrations:write"
)

// Identity is the seam between the auth and integration subsystems. The auth
// middleware produces it from a session or token; every protected handler reads
// tenant/user from here — never from request input.
type Identity struct {
	UserID     uuid.UUID
	TenantID   uuid.UUID
	Scopes     []string
	AuthMethod AuthMethod
}

// BelongsToTenant reports whether this identity is scoped to tenant t.
func (i Identity) BelongsToTenant(t uuid.UUID) bool {
	return i.TenantID != uuid.Nil && i.TenantID == t
}

// IsSamePrincipal reports whether this identity is the same tenant+user as the
// given pair. Used to confirm an OAuth callback's bound principal matches the
// caller (anti "callback bound to the wrong user").
func (i Identity) IsSamePrincipal(tenantID, userID uuid.UUID) bool {
	return i.TenantID == tenantID && i.UserID == userID
}

// IsSession reports whether the identity was authenticated via an interactive
// browser session (as opposed to a machine API token).
func (i Identity) IsSession() bool { return i.AuthMethod == AuthMethodSession }

// HasScope reports whether the identity carries scope s. Interactive sessions
// are treated as full-scope for their user (role model is out of PoC scope).
func (i Identity) HasScope(s string) bool {
	if i.IsSession() {
		return true
	}
	return slices.Contains(i.Scopes, s)
}

// Credentials is a login attempt. The tenant is derived from the matched user
// (email is globally unique), so the caller supplies only email + password.
type Credentials struct {
	Email    string
	Password string
}

// --- Users & tenants ---------------------------------------------------------

// UserStatus enumerates account states.
type UserStatus string

const (
	UserStatusActive   UserStatus = "active"
	UserStatusDisabled UserStatus = "disabled"
)

// User is a tenant-scoped account.
type User struct {
	ID           uuid.UUID
	TenantID     uuid.UUID
	Email        string
	PasswordHash string
	Status       UserStatus
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// IsActive reports whether the user may authenticate.
func (u User) IsActive() bool { return u.Status == UserStatusActive }

// Tenant is an isolation boundary that owns users and their data.
type Tenant struct {
	ID        uuid.UUID
	Slug      string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// --- API tokens (our machine keys) -------------------------------------------

// TokenMeta is the non-secret metadata of a machine API key. The plaintext key
// is shown only once at creation; only its hash is stored.
type TokenMeta struct {
	ID         uuid.UUID
	TenantID   uuid.UUID
	OwnerID    uuid.UUID
	Name       string
	Prefix     string
	Scopes     []string
	ExpiresAt  *time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
}

// IsExpired reports whether the token is past its expiry at now.
func (t TokenMeta) IsExpired(now time.Time) bool {
	return t.ExpiresAt != nil && now.After(*t.ExpiresAt)
}

// IsRevoked reports whether the token has been revoked.
func (t TokenMeta) IsRevoked() bool { return t.RevokedAt != nil }

// --- Integration credentials (provider OAuth tokens) -------------------------

// ProviderJira is the only integration provider in this PoC. It is the registry
// key persisted in integration_credentials.provider.
const ProviderJira = "jira"

// ConnectionStatus is the lifecycle state of an integration credential.
type ConnectionStatus string

const (
	StatusConnected   ConnectionStatus = "connected"
	StatusNeedsReauth ConnectionStatus = "needs_reauth"
	StatusRevoked     ConnectionStatus = "revoked"
)

// TokenSet is a freshly minted/refreshed set of provider tokens.
type TokenSet struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scopes       []string
}

// ProviderAccount identifies the third-party account a connection targets. For
// Jira: ID is the cloudid, SiteURL is the workspace base (e.g.
// https://acme.atlassian.net) used to build clickable issue links.
type ProviderAccount struct {
	ID      string
	SiteURL string
	Name    string
}

// Credential holds a tenant user's connection to a provider. Token fields are
// plaintext only in memory; they are AES-256-GCM encrypted at rest.
type Credential struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	UserID            uuid.UUID
	Provider          string
	AccessToken       string
	RefreshToken      string
	Scopes            []string
	ExternalAccountID string // Jira cloudid
	SiteURL           string // Jira workspace base URL, for clickable issue links
	AccessExpiresAt   time.Time
	RefreshLastUsedAt time.Time
	Status            ConnectionStatus
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// IsAccessTokenExpired reports whether the access token is expired (or within
// skew of expiry) at now.
func (c *Credential) IsAccessTokenExpired(now time.Time, skew time.Duration) bool {
	return !now.Before(c.AccessExpiresAt.Add(-skew))
}

// HasRefreshTokenLikelyExpired reports whether the refresh token is almost
// certainly dead due to inactivity — letting the manager short-circuit to
// reauth without a doomed API call. Only meaningful for providers that expire
// refresh tokens on inactivity (e.g. Jira: 90 days).
func (c *Credential) HasRefreshTokenLikelyExpired(now time.Time, inactivity time.Duration) bool {
	if inactivity <= 0 {
		return false
	}
	return now.After(c.RefreshLastUsedAt.Add(inactivity))
}

// NeedsReauth reports whether the connection requires the user to reconnect.
func (c *Credential) NeedsReauth() bool {
	return c.Status == StatusNeedsReauth || c.Status == StatusRevoked
}

// ConnectionInfo is the client-facing view of a connection's state.
type ConnectionInfo struct {
	Provider   string
	Status     ConnectionStatus
	Connected  bool
	Scopes     []string
	ExternalID string
}

// --- Tickets (NHI findings) --------------------------------------------------

// IdentityHubLabel tags every ticket IdentityHub creates in the provider, so the
// set is discoverable by a label search (drift reconciliation) regardless of
// which user created it. BlogDigestLabel additionally marks tickets filed by an
// automation, distinguishing them from manually reported findings.
const (
	IdentityHubLabel = "identityhub"
	BlogDigestLabel  = "blog-digest"
)

// TicketPayload is a provider-agnostic request to create a record (an NHI
// finding ticket). For Jira it maps to an issue.
type TicketPayload struct {
	ProjectKey  string
	Title       string
	Description string
	Labels      []string
}

// TicketRef points at a created record in the provider.
type TicketRef struct {
	Provider string
	IssueKey string
	URL      string
}

// ProjectRef is a provider project the user can target.
type ProjectRef struct {
	Key  string
	Name string
}

// PostSummary is a structured summary of a discovered blog post produced by the
// automation summarizer. Keeping the fields separate (rather than one prose blob)
// lets the caller compose a consistent, parseable ticket title and body.
type PostSummary struct {
	Title  string // headline, cleaned of any site/section suffix
	Source string // publication or site name, e.g. "Oasis Security"
	Type   string // content type in one word, e.g. "guide", "blog", "article"
	Body   string // prose summary: third person, no model self-reference
}

// ClientAuth is the per-call auth context a provider client needs to talk to the
// provider's API: a valid access token (already refreshed if needed) plus the
// resolved account. The client builds the API base URL from these itself.
type ClientAuth struct {
	AccessToken       string
	ExternalAccountID string // Jira cloudid
	SiteURL           string // workspace base, e.g. https://acme.atlassian.net (for issue links)
}

// ProviderTicket is one IdentityHub-labelled ticket as returned by a provider
// label search. It is the unit of drift reconciliation.
type ProviderTicket struct {
	IssueKey   string
	Title      string
	ProjectKey string
	URL        string
	CreatedAt  time.Time
}

// CreatedTicket is a cached IdentityHub ticket for the tenant's "recent tickets"
// view. The cache (Redis) mirrors the Jira label search, so this is a snapshot,
// not an authoritative record — Jira is the source of truth.
type CreatedTicket struct {
	TenantID   uuid.UUID
	Provider   string
	ProjectKey string
	IssueKey   string
	IssueURL   string
	Title      string
	CreatedAt  time.Time
}
