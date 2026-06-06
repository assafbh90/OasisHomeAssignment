package ticketreport_test

import (
	"context"
	"errors"
	"testing"
	"time"

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
	ref          domain.TicketRef
	err          error
	createCalls  int
	searchCalls  int
	searchResult []domain.ProviderTicket
	lastAuth     domain.ClientAuth
}

func (f *fakeClient) CreateIssue(_ context.Context, auth domain.ClientAuth, _ domain.TicketPayload) (domain.TicketRef, error) {
	f.createCalls++
	f.lastAuth = auth
	return f.ref, f.err
}

func (f *fakeClient) ListProjects(context.Context, domain.ClientAuth) ([]domain.ProjectRef, error) {
	return []domain.ProjectRef{{Key: "NHI", Name: "NHI"}}, nil
}

func (f *fakeClient) SearchByLabel(_ context.Context, auth domain.ClientAuth) ([]domain.ProviderTicket, error) {
	f.searchCalls++
	f.lastAuth = auth
	return f.searchResult, f.err
}

type fakeCache struct {
	added    []domain.CreatedTicket
	replaced []domain.CreatedTicket
	list     []domain.CreatedTicket
}

func (f *fakeCache) Replace(_ context.Context, _ uuid.UUID, tickets []domain.CreatedTicket) error {
	f.replaced = tickets
	return nil
}

func (f *fakeCache) Add(_ context.Context, _ uuid.UUID, t domain.CreatedTicket) error {
	f.added = append(f.added, t)
	return nil
}

func (f *fakeCache) ListByProject(context.Context, uuid.UUID, string, int) ([]domain.CreatedTicket, error) {
	return f.list, nil
}

type fakeGate struct {
	proceed  bool
	finished bool
}

func (f *fakeGate) TryAcquire(context.Context, uuid.UUID, bool) (bool, func(), error) {
	return f.proceed, func() { f.finished = true }, nil
}

type deps struct {
	tokens fakeTokens
	creds  fakeCreds
	client *fakeClient
	cache  *fakeCache
	gate   *fakeGate
}

func newService(d deps) *ticketreport.Service {
	return ticketreport.NewService(ticketreport.Deps{
		ProviderName: domain.ProviderJira, TokenManager: d.tokens, Credentials: d.creds,
		ProviderClient: d.client, TicketCache: d.cache, ReconcileGate: d.gate,
	})
}

func principal() domain.Identity {
	return domain.Identity{UserID: uuid.New(), TenantID: uuid.New(), AuthMethod: domain.AuthMethodSession}
}

func TestService_CreateTicket_HappyPath(t *testing.T) {
	t.Parallel()
	client := &fakeClient{ref: domain.TicketRef{Provider: domain.ProviderJira, IssueKey: "NHI-1", URL: "https://acme.atlassian.net/browse/NHI-1"}}
	cache := &fakeCache{}
	creds := fakeCreds{cred: &domain.Credential{ExternalAccountID: "cloud-1", SiteURL: "https://acme.atlassian.net"}}
	svc := newService(deps{tokens: fakeTokens{token: "valid-at"}, creds: creds, client: client, cache: cache, gate: &fakeGate{}})

	ref, err := svc.CreateTicket(context.Background(), principal(), domain.TicketPayload{ProjectKey: "NHI", Title: "Stale account"})
	require.NoError(t, err)
	require.Equal(t, "NHI-1", ref.IssueKey)

	// Client received assembled auth (it builds the API URL from the cloudid).
	require.Equal(t, "valid-at", client.lastAuth.AccessToken)
	require.Equal(t, "cloud-1", client.lastAuth.ExternalAccountID)
	// Ticket added to the cache so it shows immediately.
	require.Len(t, cache.added, 1)
	require.Equal(t, "NHI-1", cache.added[0].IssueKey)
}

func TestService_CreateTicket_ReauthSignals(t *testing.T) {
	t.Parallel()
	client := &fakeClient{}
	svc := newService(deps{tokens: fakeTokens{err: domain.ErrReauthRequired}, client: client, cache: &fakeCache{}, gate: &fakeGate{}})

	_, err := svc.CreateTicket(context.Background(), principal(), domain.TicketPayload{ProjectKey: "NHI", Title: "x"})

	require.ErrorIs(t, err, domain.ErrReauthRequired)
	require.Zero(t, client.createCalls, "no provider call on reauth")
}

func TestService_ListProjects_Reauth(t *testing.T) {
	t.Parallel()
	svc := newService(deps{tokens: fakeTokens{err: domain.ErrReauthRequired}, client: &fakeClient{}, cache: &fakeCache{}, gate: &fakeGate{}})

	_, err := svc.ListProjects(context.Background(), principal())
	require.ErrorIs(t, err, domain.ErrReauthRequired)
}

func TestService_ListProjects_HappyPath(t *testing.T) {
	t.Parallel()
	creds := fakeCreds{cred: &domain.Credential{ExternalAccountID: "cloud-1"}}
	svc := newService(deps{tokens: fakeTokens{token: "at"}, creds: creds, client: &fakeClient{}, cache: &fakeCache{}, gate: &fakeGate{}})

	projects, err := svc.ListProjects(context.Background(), principal())
	require.NoError(t, err)
	require.Len(t, projects, 1)
	require.Equal(t, "NHI", projects[0].Key)
}

func TestService_CreateTicket_ClientError(t *testing.T) {
	t.Parallel()
	client := &fakeClient{err: errors.New("jira 500")}
	creds := fakeCreds{cred: &domain.Credential{ExternalAccountID: "cloud-1"}}
	cache := &fakeCache{}
	svc := newService(deps{tokens: fakeTokens{token: "at"}, creds: creds, client: client, cache: cache, gate: &fakeGate{}})

	_, err := svc.CreateTicket(context.Background(), principal(), domain.TicketPayload{ProjectKey: "NHI", Title: "x"})
	require.Error(t, err)
	require.NotErrorIs(t, err, domain.ErrReauthRequired)
	require.Empty(t, cache.added, "nothing cached when the provider call fails")
}

func TestService_ListRecentTickets(t *testing.T) {
	t.Parallel()
	cache := &fakeCache{list: []domain.CreatedTicket{{IssueKey: "NHI-1", ProjectKey: "NHI"}}}
	svc := newService(deps{tokens: fakeTokens{token: "at"}, client: &fakeClient{}, cache: cache, gate: &fakeGate{}})

	got, err := svc.ListRecentTickets(context.Background(), principal(), "NHI", 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "NHI-1", got[0].IssueKey)
}

func TestService_Reconcile_ReplacesCacheFromJira(t *testing.T) {
	t.Parallel()
	client := &fakeClient{searchResult: []domain.ProviderTicket{
		{IssueKey: "NHI-1", Title: "one", ProjectKey: "NHI", URL: "u1", CreatedAt: time.Now()},
		{IssueKey: "NHI-2", Title: "two", ProjectKey: "NHI", URL: "u2", CreatedAt: time.Now()},
	}}
	cache := &fakeCache{}
	gate := &fakeGate{proceed: true}
	creds := fakeCreds{cred: &domain.Credential{ExternalAccountID: "cloud-1"}}
	svc := newService(deps{tokens: fakeTokens{token: "at"}, creds: creds, client: client, cache: cache, gate: gate})

	require.NoError(t, svc.Reconcile(context.Background(), principal(), true))
	require.Equal(t, 1, client.searchCalls)
	require.Len(t, cache.replaced, 2)
	require.Equal(t, "NHI-1", cache.replaced[0].IssueKey)
	require.True(t, gate.finished, "gate must be released")
}

func TestService_Reconcile_SkippedByGate(t *testing.T) {
	t.Parallel()
	client := &fakeClient{}
	cache := &fakeCache{}
	svc := newService(deps{tokens: fakeTokens{token: "at"}, client: client, cache: cache, gate: &fakeGate{proceed: false}})

	require.NoError(t, svc.Reconcile(context.Background(), principal(), false))
	require.Zero(t, client.searchCalls, "skipped reconcile must not hit Jira")
	require.Nil(t, cache.replaced)
}

func TestService_Reconcile_Reauth(t *testing.T) {
	t.Parallel()
	svc := newService(deps{tokens: fakeTokens{err: domain.ErrReauthRequired}, client: &fakeClient{}, cache: &fakeCache{}, gate: &fakeGate{proceed: true}})

	err := svc.Reconcile(context.Background(), principal(), true)
	require.ErrorIs(t, err, domain.ErrReauthRequired)
}
