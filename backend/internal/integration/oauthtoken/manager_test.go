package oauthtoken_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/integration/oauthtoken"
)

// fakeCredStore returns loadCred for LoadCredential and records persistence.
type fakeCredStore struct {
	loadCred *domain.Credential
	loadErr  error

	markReauthCalled bool
	persisted        bool
	persistedCred    *domain.Credential
}

func (f *fakeCredStore) LoadCredential(context.Context, uuid.UUID, uuid.UUID, string) (*domain.Credential, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	cp := *f.loadCred
	return &cp, nil
}

func (f *fakeCredStore) UpdateTokens(_ context.Context, c *domain.Credential) error {
	f.persisted = true
	f.persistedCred = c
	return nil
}

func (f *fakeCredStore) MarkNeedsReauth(context.Context, uuid.UUID, uuid.UUID, string) error {
	f.markReauthCalled = true
	return nil
}

type fakeProvider struct {
	refreshCalls int
	ts           *domain.TokenSet
	err          error
	inactivity   time.Duration
}

func (f *fakeProvider) RefreshTokens(context.Context, string) (*domain.TokenSet, error) {
	f.refreshCalls++
	return f.ts, f.err
}
func (f *fakeProvider) InactivityWindow() time.Duration { return f.inactivity }

func validCred() *domain.Credential {
	return &domain.Credential{
		TenantID: uuid.New(), UserID: uuid.New(), Provider: domain.ProviderJira,
		AccessToken: "current-at", RefreshToken: "current-rt",
		AccessExpiresAt: time.Now().Add(time.Hour), RefreshLastUsedAt: time.Now(),
		Status: domain.StatusConnected,
	}
}

func expiredCred() *domain.Credential {
	c := validCred()
	c.AccessExpiresAt = time.Now().Add(-time.Hour)
	return c
}

const inactivity = 2160 * time.Hour

func TestReactiveTokenManager_ValidToken_NoProviderCall(t *testing.T) {
	t.Parallel()
	store := &fakeCredStore{loadCred: validCred()}
	prov := &fakeProvider{inactivity: inactivity}
	m := oauthtoken.NewReactiveTokenManager(oauthtoken.Deps{Repo: store, Provider: prov, ProviderName: domain.ProviderJira, Skew: time.Minute})

	tok, err := m.FetchValidToken(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.Equal(t, "current-at", tok)
	require.Zero(t, prov.refreshCalls, "valid token must not trigger a refresh")
}

func TestReactiveTokenManager_NeedsReauth_NoProviderCall(t *testing.T) {
	t.Parallel()
	c := validCred()
	c.Status = domain.StatusNeedsReauth
	store := &fakeCredStore{loadCred: c}
	prov := &fakeProvider{inactivity: inactivity}
	m := oauthtoken.NewReactiveTokenManager(oauthtoken.Deps{Repo: store, Provider: prov, ProviderName: domain.ProviderJira, Skew: time.Minute})

	_, err := m.FetchValidToken(context.Background(), uuid.New(), uuid.New())
	require.ErrorIs(t, err, domain.ErrReauthRequired)
	require.Zero(t, prov.refreshCalls)
}

func TestReactiveTokenManager_LazyDead_ShortCircuits(t *testing.T) {
	t.Parallel()
	c := expiredCred()
	c.RefreshLastUsedAt = time.Now().Add(-100 * 24 * time.Hour) // beyond inactivity window
	store := &fakeCredStore{loadCred: c}
	prov := &fakeProvider{inactivity: inactivity}
	m := oauthtoken.NewReactiveTokenManager(oauthtoken.Deps{Repo: store, Provider: prov, ProviderName: domain.ProviderJira, Skew: time.Minute})

	_, err := m.FetchValidToken(context.Background(), uuid.New(), uuid.New())
	require.ErrorIs(t, err, domain.ErrReauthRequired)
	require.Zero(t, prov.refreshCalls, "doomed refresh must not call the provider")
	require.True(t, store.markReauthCalled)
}

func TestReactiveTokenManager_Expired_RefreshesAndPersistsRotatedToken(t *testing.T) {
	t.Parallel()
	store := &fakeCredStore{loadCred: expiredCred()}
	prov := &fakeProvider{
		inactivity: inactivity,
		ts:         &domain.TokenSet{AccessToken: "new-at", RefreshToken: "new-rt", ExpiresAt: time.Now().Add(time.Hour)},
	}
	m := oauthtoken.NewReactiveTokenManager(oauthtoken.Deps{Repo: store, Provider: prov, ProviderName: domain.ProviderJira, Skew: time.Minute})

	tok, err := m.FetchValidToken(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.Equal(t, "new-at", tok)
	require.Equal(t, 1, prov.refreshCalls)
	require.True(t, store.persisted)
	require.Equal(t, "new-rt", store.persistedCred.RefreshToken, "rotated refresh token must be persisted")
	require.Equal(t, domain.StatusConnected, store.persistedCred.Status)
}

func TestReactiveTokenManager_InvalidGrant_MarksNeedsReauth(t *testing.T) {
	t.Parallel()
	store := &fakeCredStore{loadCred: expiredCred()}
	prov := &fakeProvider{inactivity: inactivity, err: domain.ErrInvalidGrant}
	m := oauthtoken.NewReactiveTokenManager(oauthtoken.Deps{Repo: store, Provider: prov, ProviderName: domain.ProviderJira, Skew: time.Minute})

	_, err := m.FetchValidToken(context.Background(), uuid.New(), uuid.New())
	require.ErrorIs(t, err, domain.ErrReauthRequired)
	require.Equal(t, 1, prov.refreshCalls)
	require.True(t, store.markReauthCalled)
	require.False(t, store.persisted)
}

func TestReactiveTokenManager_TransientError_Propagates(t *testing.T) {
	t.Parallel()
	store := &fakeCredStore{loadCred: expiredCred()}
	prov := &fakeProvider{inactivity: inactivity, err: errors.New("503 service unavailable")}
	m := oauthtoken.NewReactiveTokenManager(oauthtoken.Deps{Repo: store, Provider: prov, ProviderName: domain.ProviderJira, Skew: time.Minute})

	_, err := m.FetchValidToken(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
	require.NotErrorIs(t, err, domain.ErrReauthRequired)
	require.False(t, store.persisted)
}

func TestReactiveTokenManager_CredentialNotFound(t *testing.T) {
	t.Parallel()
	store := &fakeCredStore{loadErr: domain.ErrCredentialNotFound}
	prov := &fakeProvider{inactivity: inactivity}
	m := oauthtoken.NewReactiveTokenManager(oauthtoken.Deps{Repo: store, Provider: prov, ProviderName: domain.ProviderJira, Skew: time.Minute})

	_, err := m.FetchValidToken(context.Background(), uuid.New(), uuid.New())
	require.ErrorIs(t, err, domain.ErrCredentialNotFound)
}
