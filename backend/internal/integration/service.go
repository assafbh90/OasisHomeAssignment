// Package integration orchestrates the outbound-connection lifecycle: OAuth
// authorization (connect/callback), credential storage, status, and disconnect.
// It is always called with the authenticated Identity, so tenant/user scoping is
// explicit end to end.
package integration

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/secret"
)

// AuthProvider is the slice of the OAuth provider the connection service needs.
type AuthProvider interface {
	Name() string
	Scopes() []string
	BuildAuthorizationURL(state, codeChallenge string) string
	ExchangeCodeForTokens(ctx context.Context, code, codeVerifier string) (*domain.TokenSet, domain.ProviderAccount, error)
}

// StateStore persists one-time OAuth state bound to {tenant,user} + PKCE verifier.
type StateStore interface {
	GenerateState(ctx context.Context, tenantID, userID uuid.UUID, codeVerifier string) (string, error)
	ConsumeState(ctx context.Context, state string) (tenantID, userID uuid.UUID, codeVerifier string, err error)
}

// CredentialRepository is the slice the connection service needs.
type CredentialRepository interface {
	SaveCredential(ctx context.Context, c *domain.Credential) error
	LoadCredential(ctx context.Context, tenantID, userID uuid.UUID, provider string) (*domain.Credential, error)
	DeleteCredential(ctx context.Context, tenantID, userID uuid.UUID, provider string) error
}

// ConnectionService implements the connect/callback/status/disconnect flow.
type ConnectionService struct {
	provider AuthProvider
	state    StateStore
	repo     CredentialRepository
	usePKCE  bool
}

// Deps are the collaborators of a ConnectionService. A struct (rather than a
// positional parameter list) keeps call sites self-documenting.
type Deps struct {
	Provider AuthProvider
	State    StateStore
	Repo     CredentialRepository
	UsePKCE  bool
}

// NewConnectionService constructs the service from its dependencies.
func NewConnectionService(d Deps) *ConnectionService {
	return &ConnectionService{provider: d.Provider, state: d.State, repo: d.Repo, usePKCE: d.UsePKCE}
}

// StartAuthorization generates PKCE + a bound state and returns the consent URL.
func (s *ConnectionService) StartAuthorization(ctx context.Context, principal domain.Identity) (string, error) {
	var verifier, challenge string
	if s.usePKCE {
		var err error
		verifier, challenge, err = generatePKCE()
		if err != nil {
			return "", err
		}
	}
	state, err := s.state.GenerateState(ctx, principal.TenantID, principal.UserID, verifier)
	if err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}
	return s.provider.BuildAuthorizationURL(state, challenge), nil
}

// CompleteAuthorization consumes the one-time state, cross-checks that it was
// bound to this very session's identity (defending the "callback bound to the
// wrong user" attack), exchanges the code, and stores the encrypted credential.
func (s *ConnectionService) CompleteAuthorization(ctx context.Context, principal domain.Identity, state, code string) error {
	tenantID, userID, verifier, err := s.state.ConsumeState(ctx, state)
	if err != nil {
		return err // ErrStateNotFound
	}
	if tenantID != principal.TenantID || userID != principal.UserID {
		return domain.ErrTenantMismatch
	}

	ts, account, err := s.provider.ExchangeCodeForTokens(ctx, code, verifier)
	if err != nil {
		return fmt.Errorf("exchange code: %w", err)
	}

	scopes := ts.Scopes
	if len(scopes) == 0 {
		scopes = s.provider.Scopes()
	}
	cred := &domain.Credential{
		TenantID:          principal.TenantID,
		UserID:            principal.UserID,
		Provider:          s.provider.Name(),
		AccessToken:       ts.AccessToken,
		RefreshToken:      ts.RefreshToken,
		Scopes:            scopes,
		ExternalAccountID: account.ID,
		SiteURL:           account.SiteURL,
		AccessExpiresAt:   ts.ExpiresAt,
		RefreshLastUsedAt: time.Now(),
		Status:            domain.StatusConnected,
	}
	if err := s.repo.SaveCredential(ctx, cred); err != nil {
		return fmt.Errorf("save credential: %w", err)
	}
	return nil
}

// DescribeConnection reports the current connection state (never an error for a
// missing connection — that is simply "not connected").
func (s *ConnectionService) DescribeConnection(ctx context.Context, principal domain.Identity) (domain.ConnectionInfo, error) {
	cred, err := s.repo.LoadCredential(ctx, principal.TenantID, principal.UserID, s.provider.Name())
	if err != nil {
		if errors.Is(err, domain.ErrCredentialNotFound) {
			return domain.ConnectionInfo{Provider: s.provider.Name(), Connected: false}, nil
		}
		return domain.ConnectionInfo{}, err
	}
	return domain.ConnectionInfo{
		Provider:   s.provider.Name(),
		Status:     cred.Status,
		Connected:  cred.Status == domain.StatusConnected,
		Scopes:     cred.Scopes,
		ExternalID: cred.ExternalAccountID,
	}, nil
}

// DisconnectIntegration removes the credential (idempotent).
func (s *ConnectionService) DisconnectIntegration(ctx context.Context, principal domain.Identity) error {
	err := s.repo.DeleteCredential(ctx, principal.TenantID, principal.UserID, s.provider.Name())
	if err != nil && !errors.Is(err, domain.ErrCredentialNotFound) {
		return err
	}
	return nil
}

// generatePKCE returns a (verifier, S256-challenge) pair.
func generatePKCE() (string, string, error) {
	verifier, err := secret.NewToken(secret.TokenBytes)
	if err != nil {
		return "", "", fmt.Errorf("generate pkce verifier: %w", err)
	}
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}
