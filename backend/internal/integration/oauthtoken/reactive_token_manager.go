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
	credentials  CredentialStore
	provider     RefreshProvider
	providerName string
	refreshSkew  time.Duration // refresh this long before actual expiry
}

// Deps are the collaborators of a ReactiveTokenManager. RefreshSkew triggers a
// refresh slightly before actual expiry.
type Deps struct {
	Credentials  CredentialStore
	Provider     RefreshProvider
	ProviderName string
	RefreshSkew  time.Duration
}

// NewReactiveTokenManager constructs the manager from its dependencies.
func NewReactiveTokenManager(d Deps) *ReactiveTokenManager {
	return &ReactiveTokenManager{
		credentials:  d.Credentials,
		provider:     d.Provider,
		providerName: d.ProviderName,
		refreshSkew:  d.RefreshSkew,
	}
}

// FetchValidToken returns a usable access token, refreshing it on demand. It
// returns ErrReauthRequired when the connection must be reconnected,
// ErrCredentialNotFound when absent, or a wrapped error for transient failures.
func (m *ReactiveTokenManager) FetchValidToken(ctx context.Context, tenantID, userID uuid.UUID) (string, error) {
	credential, err := m.credentials.LoadCredential(ctx, tenantID, userID, m.providerName)
	if err != nil {
		return "", err
	}
	if credential.NeedsReauth() {
		return "", domain.ErrReauthRequired
	}
	now := time.Now()
	// Fast path: a still-valid access token never triggers a provider call.
	if !credential.IsAccessTokenExpired(now, m.refreshSkew) {
		return credential.AccessToken, nil
	}
	// Lazy-dead: a refresh token that can't possibly work skips the API entirely.
	if credential.HasRefreshTokenLikelyExpired(now, m.provider.InactivityWindow()) {
		m.markReauth(ctx, tenantID, userID)
		return "", domain.ErrReauthRequired
	}

	tokenSet, err := m.provider.RefreshTokens(ctx, credential.RefreshToken)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidGrant) {
			m.markReauth(ctx, tenantID, userID)
			return "", domain.ErrReauthRequired
		}
		return "", fmt.Errorf("refresh tokens: %w", err)
	}

	credential.AccessToken = tokenSet.AccessToken
	credential.AccessExpiresAt = tokenSet.ExpiresAt
	if tokenSet.RefreshToken != "" {
		credential.RefreshToken = tokenSet.RefreshToken // persist the rotated refresh token
	}
	if len(tokenSet.Scopes) > 0 {
		credential.Scopes = tokenSet.Scopes
	}
	credential.RefreshLastUsedAt = now
	credential.Status = domain.StatusConnected
	if err := m.credentials.UpdateTokens(ctx, credential); err != nil {
		return "", fmt.Errorf("persist refreshed tokens: %w", err)
	}
	return credential.AccessToken, nil
}

func (m *ReactiveTokenManager) markReauth(ctx context.Context, tenantID, userID uuid.UUID) {
	if err := m.credentials.MarkNeedsReauth(ctx, tenantID, userID, m.providerName); err != nil {
		logging.FromContext(ctx).Warn("mark needs_reauth failed", logging.Err(err))
	}
}
