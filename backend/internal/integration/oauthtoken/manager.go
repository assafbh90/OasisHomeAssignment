package oauthtoken

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/logging"
)

// CredentialStore is the slice of the credential repository the manager needs.
type CredentialStore interface {
	LoadCredential(ctx context.Context, tenantID, userID uuid.UUID, provider string) (*domain.Credential, error)
	UpdateTokens(ctx context.Context, c *domain.Credential) error
	MarkNeedsReauth(ctx context.Context, tenantID, userID uuid.UUID, provider string) error
}

// RefreshProvider is the slice of the OAuth provider the manager needs.
type RefreshProvider interface {
	RefreshTokens(ctx context.Context, refreshToken string) (*domain.TokenSet, error)
	InactivityWindow() time.Duration
}

// ReactiveTokenManager returns valid provider access tokens, refreshing lazily
// (only at use time) and never via a background warmer.
type ReactiveTokenManager struct {
	repo     CredentialStore
	provider RefreshProvider
	name     string
	skew     time.Duration
}

// Deps are the collaborators of a ReactiveTokenManager. Skew triggers refresh
// slightly before actual expiry.
type Deps struct {
	Repo         CredentialStore
	Provider     RefreshProvider
	ProviderName string
	Skew         time.Duration
}

// NewReactiveTokenManager constructs the manager from its dependencies.
func NewReactiveTokenManager(d Deps) *ReactiveTokenManager {
	return &ReactiveTokenManager{repo: d.Repo, provider: d.Provider, name: d.ProviderName, skew: d.Skew}
}

// FetchValidToken returns a usable access token, refreshing it on demand. It
// returns ErrReauthRequired when the connection must be reconnected,
// ErrCredentialNotFound when absent, or a wrapped error for transient failures.
func (m *ReactiveTokenManager) FetchValidToken(ctx context.Context, tenantID, userID uuid.UUID) (string, error) {
	cred, err := m.repo.LoadCredential(ctx, tenantID, userID, m.name)
	if err != nil {
		return "", err
	}
	if cred.NeedsReauth() {
		return "", domain.ErrReauthRequired
	}
	now := time.Now()
	// Fast path: a still-valid access token never triggers a provider call.
	if !cred.IsAccessTokenExpired(now, m.skew) {
		return cred.AccessToken, nil
	}
	// Lazy-dead: a refresh token that can't possibly work skips the API entirely.
	if cred.HasRefreshTokenLikelyExpired(now, m.provider.InactivityWindow()) {
		m.markReauth(ctx, tenantID, userID)
		return "", domain.ErrReauthRequired
	}

	ts, err := m.provider.RefreshTokens(ctx, cred.RefreshToken)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidGrant) {
			m.markReauth(ctx, tenantID, userID)
			return "", domain.ErrReauthRequired
		}
		return "", fmt.Errorf("refresh tokens: %w", err)
	}

	cred.AccessToken = ts.AccessToken
	cred.AccessExpiresAt = ts.ExpiresAt
	if ts.RefreshToken != "" {
		cred.RefreshToken = ts.RefreshToken // persist the rotated refresh token
	}
	if len(ts.Scopes) > 0 {
		cred.Scopes = ts.Scopes
	}
	cred.RefreshLastUsedAt = now
	cred.Status = domain.StatusConnected
	if err := m.repo.UpdateTokens(ctx, cred); err != nil {
		return "", fmt.Errorf("persist refreshed tokens: %w", err)
	}
	return cred.AccessToken, nil
}

func (m *ReactiveTokenManager) markReauth(ctx context.Context, tenantID, userID uuid.UUID) {
	if err := m.repo.MarkNeedsReauth(ctx, tenantID, userID, m.name); err != nil {
		logging.FromContext(ctx).Warn("mark needs_reauth failed", logging.Err(err))
	}
}
