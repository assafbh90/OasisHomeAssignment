// Package ticketreport holds the NHI business logic: reporting findings as Jira
// tickets, listing target projects, the recent-tickets view, and reconciling the
// tenant's ticket cache against Jira (the source of truth). It depends on the
// integration subsystem for a valid token + a provider client.
package ticketreport

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/samber/lo"

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

// ReconcileGate throttles + single-flights reconciliation per tenant. TryAcquire
// reports whether to proceed; release frees the lock and stamps the throttle.
type ReconcileGate interface {
	TryAcquire(ctx context.Context, tenantID uuid.UUID, force bool) (proceed bool, release func(), err error)
}

// Service implements the NHI finding-ticket use cases.
type Service struct {
	providerName   string
	tokenManager   TokenManager
	credentials    CredentialReader
	providerClient Client
	ticketCache    TicketCache
	reconcileGate  ReconcileGate
}

// Deps are the collaborators of a Service. Passing them as a struct (rather than
// a long positional parameter list) keeps call sites self-documenting and makes
// it impossible to silently transpose the several same-typed deps.
type Deps struct {
	ProviderName   string
	TokenManager   TokenManager
	Credentials    CredentialReader
	ProviderClient Client
	TicketCache    TicketCache
	ReconcileGate  ReconcileGate
}

// NewService constructs the service from its dependencies.
func NewService(d Deps) *Service {
	return &Service{
		providerName: d.ProviderName, tokenManager: d.TokenManager, credentials: d.Credentials,
		providerClient: d.ProviderClient, ticketCache: d.TicketCache, reconcileGate: d.ReconcileGate,
	}
}

// ListProjects returns the user's provider projects (project picker). It returns
// domain.ErrReauthRequired when the connection needs reconnecting.
func (s *Service) ListProjects(ctx context.Context, principal domain.Identity) ([]domain.ProjectRef, error) {
	auth, err := s.resolveAuth(ctx, principal)
	if err != nil {
		return nil, err
	}
	return s.providerClient.ListProjects(ctx, auth)
}

// CreateTicket creates an NHI finding ticket (tagged with the IdentityHub label)
// and adds it to the cache so it shows immediately. It returns
// domain.ErrReauthRequired when the connection needs reconnecting.
func (s *Service) CreateTicket(ctx context.Context, principal domain.Identity, payload domain.TicketPayload) (domain.TicketRef, error) {
	auth, err := s.resolveAuth(ctx, principal)
	if err != nil {
		return domain.TicketRef{}, err
	}

	ref, err := s.providerClient.CreateIssue(ctx, auth, payload)
	if err != nil {
		return domain.TicketRef{}, err
	}

	ticket := domain.CreatedTicket{
		TenantID:   principal.TenantID,
		Provider:   s.providerName,
		ProjectKey: payload.ProjectKey,
		IssueKey:   ref.IssueKey,
		IssueURL:   ref.URL,
		Title:      payload.Title,
		CreatedAt:  time.Now(),
	}
	if err := s.ticketCache.Add(ctx, principal.TenantID, ticket); err != nil {
		// The ticket exists in Jira; failing to cache it only delays it appearing
		// in the recent view until the next reconcile. Log and return the ref.
		logging.FromContext(ctx).Warn("cache created ticket failed", logging.Err(err))
	}
	return ref, nil
}

// ListRecentTickets returns the tenant's cached IdentityHub tickets for a project
// (no provider call). The cache is kept fresh by Reconcile.
func (s *Service) ListRecentTickets(ctx context.Context, principal domain.Identity, projectKey string, limit int) ([]domain.CreatedTicket, error) {
	return s.ticketCache.ListByProject(ctx, principal.TenantID, projectKey, limit)
}

// Reconcile refreshes the tenant's ticket cache from Jira's IdentityHub label
// search (Jira is the source of truth). It is throttled + single-flighted by the
// gate; when skipped it returns nil. force bypasses the throttle (refresh button).
func (s *Service) Reconcile(ctx context.Context, principal domain.Identity, force bool) error {
	proceed, release, err := s.reconcileGate.TryAcquire(ctx, principal.TenantID, force)
	if err != nil {
		return err
	}
	if !proceed {
		return nil // reconciled recently, or another reconcile is in flight
	}
	defer release()

	auth, err := s.resolveAuth(ctx, principal)
	if err != nil {
		return err
	}
	found, err := s.providerClient.SearchByLabel(ctx, auth)
	if err != nil {
		return err
	}

	tickets := lo.Map(found, func(t domain.ProviderTicket, _ int) domain.CreatedTicket {
		return domain.CreatedTicket{
			TenantID:   principal.TenantID,
			Provider:   s.providerName,
			ProjectKey: t.ProjectKey,
			IssueKey:   t.IssueKey,
			IssueURL:   t.URL,
			Title:      t.Title,
			CreatedAt:  t.CreatedAt,
		}
	})
	return s.ticketCache.Replace(ctx, principal.TenantID, tickets)
}

// EnsureConnected reports whether the principal's provider connection is usable
// right now, returning domain.ErrReauthRequired if it needs reconnecting. It does
// no provider API call beyond a possible token refresh, so callers (e.g. the
// automation runner) can cheaply check before doing expensive work.
func (s *Service) EnsureConnected(ctx context.Context, principal domain.Identity) error {
	_, err := s.tokenManager.FetchValidToken(ctx, principal.TenantID, principal.UserID)
	return err
}

// resolveAuth fetches a valid token + credential and assembles ClientAuth.
func (s *Service) resolveAuth(ctx context.Context, principal domain.Identity) (domain.ClientAuth, error) {
	token, err := s.tokenManager.FetchValidToken(ctx, principal.TenantID, principal.UserID)
	if err != nil {
		return domain.ClientAuth{}, err
	}
	cred, err := s.credentials.LoadCredential(ctx, principal.TenantID, principal.UserID, s.providerName)
	if err != nil {
		return domain.ClientAuth{}, err
	}
	return domain.ClientAuth{
		AccessToken:       token,
		ExternalAccountID: cred.ExternalAccountID,
		SiteURL:           cred.SiteURL,
	}, nil
}
