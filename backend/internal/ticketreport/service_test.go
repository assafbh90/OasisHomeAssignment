package ticketreport_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/ticketreport"
)

type fakeTokens struct {
	token string
	err   error
}

func (f fakeTokens) FetchValidToken(context.Context, uuid.UUID, uuid.UUID) (string, error) {
	return f.token, f.err
}

type fakeCreds struct{ cred *domain.Credential }

func (f fakeCreds) LoadCredential(context.Context, uuid.UUID, uuid.UUID, string) (*domain.Credential, error) {
	if f.cred == nil {
		return nil, domain.ErrCredentialNotFound
	}
	return f.cred, nil
}

type fakeClient struct {
	ref         domain.TicketRef
	err         error
	createCalls int
	lastAuth    domain.ClientAuth
}

func (f *fakeClient) CreateIssue(_ context.Context, auth domain.ClientAuth, _ domain.TicketPayload) (domain.TicketRef, error) {
	f.createCalls++
	f.lastAuth = auth
	return f.ref, f.err
}

func (f *fakeClient) ListProjects(context.Context, domain.ClientAuth) ([]domain.ProjectRef, error) {
	return []domain.ProjectRef{{Key: "NHI", Name: "NHI"}}, nil
}

type fakeTicketRepo struct{ saved *domain.CreatedTicket }

func (f *fakeTicketRepo) SaveTicket(_ context.Context, t *domain.CreatedTicket) error {
	f.saved = t
	return nil
}

func (f *fakeTicketRepo) ListRecentByProject(context.Context, uuid.UUID, uuid.UUID, string, int) ([]domain.CreatedTicket, error) {
	return nil, nil
}

func newService(tokens ticketreport.TokenManager, creds ticketreport.CredentialReader, cl ticketreport.Client, tickets ticketreport.TicketRepository) *ticketreport.Service {
	return ticketreport.NewService(ticketreport.Deps{
		Provider: domain.ProviderJira, Tokens: tokens, Creds: creds,
		Client: cl, Tickets: tickets,
	})
}

func principal() domain.Identity {
	return domain.Identity{UserID: uuid.New(), TenantID: uuid.New(), AuthMethod: domain.AuthMethodSession}
}

func TestService_CreateTicket_HappyPath(t *testing.T) {
	t.Parallel()
	conn := &fakeClient{ref: domain.TicketRef{Provider: domain.ProviderJira, IssueKey: "NHI-1", URL: "https://acme.atlassian.net/browse/NHI-1"}}
	tickets := &fakeTicketRepo{}
	creds := fakeCreds{cred: &domain.Credential{ExternalAccountID: "cloud-1", SiteURL: "https://acme.atlassian.net"}}
	svc := newService(fakeTokens{token: "valid-at"}, creds, conn, tickets)

	ref, err := svc.CreateTicket(context.Background(), principal(), domain.TicketPayload{ProjectKey: "NHI", Title: "Stale account"})
	require.NoError(t, err)
	require.Equal(t, "NHI-1", ref.IssueKey)

	// Client received assembled auth (it builds the API URL from the cloudid).
	require.Equal(t, "valid-at", conn.lastAuth.AccessToken)
	require.Equal(t, "cloud-1", conn.lastAuth.ExternalAccountID)
	// Ticket recorded for the recent view.
	require.NotNil(t, tickets.saved)
	require.Equal(t, "NHI-1", tickets.saved.IssueKey)
}

func TestService_CreateTicket_ReauthSignals(t *testing.T) {
	t.Parallel()
	conn := &fakeClient{}
	svc := newService(fakeTokens{err: domain.ErrReauthRequired}, fakeCreds{}, conn, &fakeTicketRepo{})

	_, err := svc.CreateTicket(context.Background(), principal(), domain.TicketPayload{ProjectKey: "NHI", Title: "x"})

	require.ErrorIs(t, err, domain.ErrReauthRequired)
	require.Zero(t, conn.createCalls, "no provider call on reauth")
}

func TestService_ListProjects_Reauth(t *testing.T) {
	t.Parallel()
	svc := newService(fakeTokens{err: domain.ErrReauthRequired}, fakeCreds{}, &fakeClient{}, &fakeTicketRepo{})

	_, err := svc.ListProjects(context.Background(), principal())
	require.ErrorIs(t, err, domain.ErrReauthRequired)
}

func TestService_ListProjects_HappyPath(t *testing.T) {
	t.Parallel()
	creds := fakeCreds{cred: &domain.Credential{ExternalAccountID: "cloud-1"}}
	svc := newService(fakeTokens{token: "at"}, creds, &fakeClient{}, &fakeTicketRepo{})

	projects, err := svc.ListProjects(context.Background(), principal())
	require.NoError(t, err)
	require.Len(t, projects, 1)
	require.Equal(t, "NHI", projects[0].Key)
}

func TestService_CreateTicket_ConnectorError(t *testing.T) {
	t.Parallel()
	conn := &fakeClient{err: errors.New("jira 500")}
	creds := fakeCreds{cred: &domain.Credential{ExternalAccountID: "cloud-1"}}
	tickets := &fakeTicketRepo{}
	svc := newService(fakeTokens{token: "at"}, creds, conn, tickets)

	_, err := svc.CreateTicket(context.Background(), principal(), domain.TicketPayload{ProjectKey: "NHI", Title: "x"})
	require.Error(t, err)
	require.NotErrorIs(t, err, domain.ErrReauthRequired)
	require.Nil(t, tickets.saved, "nothing persisted when the provider call fails")
}

func TestService_ListRecentTickets(t *testing.T) {
	t.Parallel()
	svc := newService(fakeTokens{token: "at"}, fakeCreds{}, &fakeClient{}, &fakeTicketRepo{})
	got, err := svc.ListRecentTickets(context.Background(), principal(), "NHI", 10)
	require.NoError(t, err)
	require.Empty(t, got)
}
