// Package ticketreport holds the NHI business logic: reporting findings as
// tickets, listing target projects, and the recent-tickets view. It depends on
// the integration subsystem for a valid token + connector, but owns the
// created_tickets domain.
package ticketreport

import (
	"context"

	"github.com/google/uuid"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/logging"
)

// TokenManager yields a valid provider access token (reactive refresh).
type TokenManager interface {
	FetchValidToken(ctx context.Context, tenantID, userID uuid.UUID) (string, error)
}

// CredentialReader loads a credential (for cloudid + site URL).
type CredentialReader interface {
	LoadCredential(ctx context.Context, tenantID, userID uuid.UUID, provider string) (*domain.Credential, error)
}

// Client performs the provider operations the reporter needs.
type Client interface {
	CreateIssue(ctx context.Context, auth domain.ClientAuth, payload domain.TicketPayload) (domain.TicketRef, error)
	ListProjects(ctx context.Context, auth domain.ClientAuth) ([]domain.ProjectRef, error)
}

// TicketRepository persists/reads app-created tickets.
type TicketRepository interface {
	SaveTicket(ctx context.Context, t *domain.CreatedTicket) error
	ListRecentByProject(ctx context.Context, tenantID, userID uuid.UUID, projectKey string, limit int) ([]domain.CreatedTicket, error)
}

// Service implements the NHI finding-ticket use cases.
type Service struct {
	provider string
	tokens   TokenManager
	creds    CredentialReader
	client   Client
	tickets  TicketRepository
}

// Deps are the collaborators of a Service. Passing them as a struct
// (rather than a long positional parameter list) keeps call sites self-documenting
// and makes it impossible to silently transpose the several same-typed deps.
type Deps struct {
	Provider string
	Tokens   TokenManager
	Creds    CredentialReader
	Client   Client
	Tickets  TicketRepository
}

// NewService constructs the service from its dependencies.
func NewService(d Deps) *Service {
	return &Service{
		provider: d.Provider, tokens: d.Tokens, creds: d.Creds,
		client: d.Client, tickets: d.Tickets,
	}
}

// ListProjects returns the user's provider projects (project picker). It returns
// domain.ErrReauthRequired when the connection needs reconnecting.
func (s *Service) ListProjects(ctx context.Context, principal domain.Identity) ([]domain.ProjectRef, error) {
	auth, err := s.resolveAuth(ctx, principal)
	if err != nil {
		return nil, err
	}
	return s.client.ListProjects(ctx, auth)
}

// CreateTicket creates an NHI finding ticket and records it for the recent view.
// It returns domain.ErrReauthRequired when the connection needs reconnecting.
func (s *Service) CreateTicket(ctx context.Context, principal domain.Identity, payload domain.TicketPayload) (domain.TicketRef, error) {
	auth, err := s.resolveAuth(ctx, principal)
	if err != nil {
		return domain.TicketRef{}, err
	}

	ref, err := s.client.CreateIssue(ctx, auth, payload)
	if err != nil {
		return domain.TicketRef{}, err
	}

	ticket := &domain.CreatedTicket{
		TenantID:   principal.TenantID,
		UserID:     principal.UserID,
		Provider:   s.provider,
		ProjectKey: payload.ProjectKey,
		IssueKey:   ref.IssueKey,
		IssueURL:   ref.URL,
		Title:      payload.Title,
	}
	if err := s.tickets.SaveTicket(ctx, ticket); err != nil {
		// The ticket exists in the provider; failing to record it locally only
		// affects the recent-tickets view. Log and return the ref.
		logging.FromContext(ctx).Error("save created ticket failed", logging.Err(err))
	}
	return ref, nil
}

// ListRecentTickets returns app-created tickets for a project from local storage
// (no provider call).
func (s *Service) ListRecentTickets(ctx context.Context, principal domain.Identity, projectKey string, limit int) ([]domain.CreatedTicket, error) {
	return s.tickets.ListRecentByProject(ctx, principal.TenantID, principal.UserID, projectKey, limit)
}

// resolveAuth fetches a valid token + credential and assembles ClientAuth.
func (s *Service) resolveAuth(ctx context.Context, principal domain.Identity) (domain.ClientAuth, error) {
	token, err := s.tokens.FetchValidToken(ctx, principal.TenantID, principal.UserID)
	if err != nil {
		return domain.ClientAuth{}, err
	}
	cred, err := s.creds.LoadCredential(ctx, principal.TenantID, principal.UserID, s.provider)
	if err != nil {
		return domain.ClientAuth{}, err
	}
	return domain.ClientAuth{
		AccessToken:       token,
		ExternalAccountID: cred.ExternalAccountID,
		SiteURL:           cred.SiteURL,
	}, nil
}
