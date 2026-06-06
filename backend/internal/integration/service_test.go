package integration_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/integration"
)

type fakeAuthProvider struct {
	ts      *domain.TokenSet
	account domain.ProviderAccount
	err     error
}

func (f *fakeAuthProvider) Name() string     { return domain.ProviderJira }
func (f *fakeAuthProvider) Scopes() []string { return []string{"read:jira-work"} }
func (f *fakeAuthProvider) BuildAuthorizationURL(state, challenge string) string {
	return "https://auth.example.com/authorize?state=" + state + "&code_challenge=" + challenge
}

func (f *fakeAuthProvider) ExchangeCodeForTokens(context.Context, string, string) (*domain.TokenSet, domain.ProviderAccount, error) {
	return f.ts, f.account, f.err
}

type fakeStateStore struct {
	state    string
	tenantID uuid.UUID
	userID   uuid.UUID
	verifier string
	consErr  error
}

func (f *fakeStateStore) GenerateState(_ context.Context, t, u uuid.UUID, v string) (string, error) {
	f.tenantID, f.userID, f.verifier = t, u, v
	return f.state, nil
}

func (f *fakeStateStore) ConsumeState(context.Context, string) (uuid.UUID, uuid.UUID, string, error) {
	return f.tenantID, f.userID, f.verifier, f.consErr
}

type fakeCredRepo struct {
	saved   *domain.Credential
	deleted bool
}

func (f *fakeCredRepo) SaveCredential(_ context.Context, c *domain.Credential) error {
	f.saved = c
	return nil
}

func (f *fakeCredRepo) LoadCredential(context.Context, uuid.UUID, uuid.UUID, string) (*domain.Credential, error) {
	if f.saved == nil {
		return nil, domain.ErrCredentialNotFound
	}
	return f.saved, nil
}

func (f *fakeCredRepo) DeleteCredential(context.Context, uuid.UUID, uuid.UUID, string) error {
	f.deleted = true
	return nil
}

func principal() domain.Identity {
	return domain.Identity{UserID: uuid.New(), TenantID: uuid.New(), AuthMethod: domain.AuthMethodSession}
}

func TestConnectionService_CompleteAuthorization_Success(t *testing.T) {
	t.Parallel()
	p := principal()
	state := &fakeStateStore{state: "st"}
	// Bind the state to this principal (as StartAuthorization would).
	_, _ = state.GenerateState(context.Background(), p.TenantID, p.UserID, "verifier")
	repo := &fakeCredRepo{}
	prov := &fakeAuthProvider{
		ts:      &domain.TokenSet{AccessToken: "at", RefreshToken: "rt", ExpiresAt: time.Now().Add(time.Hour)},
		account: domain.ProviderAccount{ID: "cloud-1", SiteURL: "https://acme.atlassian.net", Name: "acme"},
	}
	svc := integration.NewConnectionService(integration.Deps{Provider: prov, State: state, Repo: repo, UsePKCE: true})

	err := svc.CompleteAuthorization(context.Background(), p, "st", "the-code")
	require.NoError(t, err)
	require.NotNil(t, repo.saved)
	require.Equal(t, p.TenantID, repo.saved.TenantID)
	require.Equal(t, "cloud-1", repo.saved.ExternalAccountID)
	require.Equal(t, "https://acme.atlassian.net", repo.saved.SiteURL)
	require.Equal(t, domain.StatusConnected, repo.saved.Status)
}

func TestConnectionService_CompleteAuthorization_IdentityCrossCheck(t *testing.T) {
	t.Parallel()
	p := principal()
	// State bound to a DIFFERENT user than the session principal.
	state := &fakeStateStore{state: "st"}
	_, _ = state.GenerateState(context.Background(), uuid.New(), uuid.New(), "verifier")
	repo := &fakeCredRepo{}
	prov := &fakeAuthProvider{ts: &domain.TokenSet{}, account: domain.ProviderAccount{}}
	svc := integration.NewConnectionService(integration.Deps{Provider: prov, State: state, Repo: repo, UsePKCE: true})

	err := svc.CompleteAuthorization(context.Background(), p, "st", "the-code")
	require.ErrorIs(t, err, domain.ErrTenantMismatch)
	require.Nil(t, repo.saved, "must not store a credential on identity mismatch")
}

func TestConnectionService_CompleteAuthorization_UnknownState(t *testing.T) {
	t.Parallel()
	p := principal()
	state := &fakeStateStore{consErr: domain.ErrStateNotFound}
	svc := integration.NewConnectionService(integration.Deps{Provider: &fakeAuthProvider{}, State: state, Repo: &fakeCredRepo{}, UsePKCE: true})

	err := svc.CompleteAuthorization(context.Background(), p, "unknown", "code")
	require.ErrorIs(t, err, domain.ErrStateNotFound)
}

func TestConnectionService_DescribeConnection_NotConnected(t *testing.T) {
	t.Parallel()
	svc := integration.NewConnectionService(integration.Deps{Provider: &fakeAuthProvider{}, State: &fakeStateStore{}, Repo: &fakeCredRepo{}, UsePKCE: true})
	info, err := svc.DescribeConnection(context.Background(), principal())
	require.NoError(t, err)
	require.False(t, info.Connected)
	require.Equal(t, domain.ProviderJira, info.Provider)
}

func TestConnectionService_DescribeConnection_Connected(t *testing.T) {
	t.Parallel()
	repo := &fakeCredRepo{saved: &domain.Credential{Status: domain.StatusConnected, Scopes: []string{"read:jira-work"}}}
	svc := integration.NewConnectionService(integration.Deps{Provider: &fakeAuthProvider{}, State: &fakeStateStore{}, Repo: repo, UsePKCE: true})
	info, err := svc.DescribeConnection(context.Background(), principal())
	require.NoError(t, err)
	require.True(t, info.Connected)
}

func TestConnectionService_StartAuthorization(t *testing.T) {
	t.Parallel()
	state := &fakeStateStore{state: "st-1"}
	svc := integration.NewConnectionService(integration.Deps{Provider: &fakeAuthProvider{}, State: state, Repo: &fakeCredRepo{}, UsePKCE: true})
	url, err := svc.StartAuthorization(context.Background(), principal())
	require.NoError(t, err)
	require.Contains(t, url, "state=st-1")
	require.Contains(t, url, "code_challenge=") // PKCE enabled
}

func TestConnectionService_Disconnect(t *testing.T) {
	t.Parallel()
	repo := &fakeCredRepo{saved: &domain.Credential{Status: domain.StatusConnected}}
	svc := integration.NewConnectionService(integration.Deps{Provider: &fakeAuthProvider{}, State: &fakeStateStore{}, Repo: repo, UsePKCE: true})
	require.NoError(t, svc.DisconnectIntegration(context.Background(), principal()))
	require.True(t, repo.deleted)
}
