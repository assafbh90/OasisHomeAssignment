// Package ticketreport holds the NHI business logic: reporting findings as Jira
// tickets, listing target projects, the recent-tickets view, and reconciling the
// tenant's ticket cache against Jira (the source of truth). It depends on the
// integration subsystem for a valid token + a provider client.
package ticketreport

import (
	"context"
	"time"

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

// Client performs the provider operations the reporter needs. SearchByLabel
// returns every IdentityHub-labelled ticket on the connected site (discovery).
type Client interface {
	CreateIssue(ctx context.Context, auth domain.ClientAuth, payload domain.TicketPayload) (domain.TicketRef, error)
	ListProjects(ctx context.Context, auth domain.ClientAuth) ([]domain.ProjectRef, error)
	SearchByLabel(ctx context.Context, auth domain.ClientAuth) ([]domain.ProviderTicket, error)
}

// TicketCache is the tenant-scoped cache of IdentityHub tickets (Redis).
type TicketCache interface {
	Replace(ctx context.Context, tenantID uuid.UUID, tickets []domain.CreatedTicket) error
	Add(ctx context.Context, tenantID uuid.UUID, ticket domain.CreatedTicket) error
	ListByProject(ctx context.Context, tenantID uuid.UUID, projectKey string, limit int) ([]domain.CreatedTicket, error)
}

// ReconcileGate throttles + single-flights reconciliation per tenant. Begin
// reports whether to proceed; finish releases the lock and stamps the throttle.
type ReconcileGate interface {
	Begin(ctx context.Context, tenantID uuid.UUID, force bool) (proceed bool, finish func(), err error)
}

// Service implements the NHI finding-ticket use cases.
type Service struct {
	provider string
	tokens   TokenManager
	creds    CredentialReader
	client   Client
	cache    TicketCache
	gate     ReconcileGate
}

// Deps are the collaborators of a Service. Passing them as a struct (rather than
// a long positional parameter list) keeps call sites self-documenting and makes
// it impossible to silently transpose the several same-typed deps.
type Deps struct {
	Provider string
	Tokens   TokenManager
	Creds    CredentialReader
	Client   Client
	Cache    TicketCache
	Gate     ReconcileGate
}

// NewService constructs the service from its dependencies.
func NewService(d Deps) *Service {
	return &Service{
		provider: d.Provider, tokens: d.Tokens, creds: d.Creds,
		client: d.Client, cache: d.Cache, gate: d.Gate,
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

// CreateTicket creates an NHI finding ticket (tagged with the IdentityHub label)
// and adds it to the cache so it shows immediately. It returns
// domain.ErrReauthRequired when the connection needs reconnecting.
func (s *Service) CreateTicket(ctx context.Context, principal domain.Identity, payload domain.TicketPayload) (domain.TicketRef, error) {
	auth, err := s.resolveAuth(ctx, principal)
	if err != nil {
		return domain.TicketRef{}, err
	}

	ref, err := s.client.CreateIssue(ctx, auth, payload)
	if err != nil {
		return domain.TicketRef{}, err
	}

	ticket := domain.CreatedTicket{
		TenantID:   principal.TenantID,
		Provider:   s.provider,
		ProjectKey: payload.ProjectKey,
		IssueKey:   ref.IssueKey,
		IssueURL:   ref.URL,
		Title:      payload.Title,
		CreatedAt:  time.Now(),
	}
	if err := s.cache.Add(ctx, principal.TenantID, ticket); err != nil {
		// The ticket exists in Jira; failing to cache it only delays it appearing
		// in the recent view until the next reconcile. Log and return the ref.
		logging.FromContext(ctx).Warn("cache created ticket failed", logging.Err(err))
	}
	return ref, nil
}

// ListRecentTickets returns the tenant's cached IdentityHub tickets for a project
// (no provider call). The cache is kept fresh by Reconcile.
func (s *Service) ListRecentTickets(ctx context.Context, principal domain.Identity, projectKey string, limit int) ([]domain.CreatedTicket, error) {
	return s.cache.ListByProject(ctx, principal.TenantID, projectKey, limit)
}

// Reconcile refreshes the tenant's ticket cache from Jira's IdentityHub label
// search (Jira is the source of truth). It is throttled + single-flighted by the
// gate; when skipped it returns nil. force bypasses the throttle (refresh button).
func (s *Service) Reconcile(ctx context.Context, principal domain.Identity, force bool) error {
	proceed, finish, err := s.gate.Begin(ctx, principal.TenantID, force)
	if err != nil {
		return err
	}
	if !proceed {
		return nil // reconciled recently, or another reconcile is in flight
	}
	defer finish()

	auth, err := s.resolveAuth(ctx, principal)
	if err != nil {
		return err
	}
	found, err := s.client.SearchByLabel(ctx, auth)
	if err != nil {
		return err
	}

	tickets := make([]domain.CreatedTicket, 0, len(found))
	for _, t := range found {
		tickets = append(tickets, domain.CreatedTicket{
			TenantID:   principal.TenantID,
			Provider:   s.provider,
			ProjectKey: t.ProjectKey,
			IssueKey:   t.IssueKey,
			IssueURL:   t.URL,
			Title:      t.Title,
			CreatedAt:  t.CreatedAt,
		})
	}
	return s.cache.Replace(ctx, principal.TenantID, tickets)
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
