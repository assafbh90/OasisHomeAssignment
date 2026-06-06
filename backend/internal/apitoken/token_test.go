package apitoken_test

import (
	"context"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/apitoken"
	"github.com/assafbh/identityhub/internal/domain"
)

type fakeRepo struct {
	byHash   map[string]domain.TokenMeta // hex(hash) -> meta
	hashByID map[uuid.UUID]string
	touched  int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{byHash: map[string]domain.TokenMeta{}, hashByID: map[uuid.UUID]string{}}
}

func (f *fakeRepo) SaveToken(_ context.Context, meta *domain.TokenMeta, hash []byte) error {
	meta.ID = uuid.New()
	meta.CreatedAt = time.Now()
	f.byHash[hex.EncodeToString(hash)] = *meta
	f.hashByID[meta.ID] = hex.EncodeToString(hash)
	return nil
}

func (f *fakeRepo) FindByHash(_ context.Context, hash []byte) (domain.TokenMeta, error) {
	m, ok := f.byHash[hex.EncodeToString(hash)]
	if !ok {
		return domain.TokenMeta{}, domain.ErrTokenNotFound
	}
	return m, nil
}

func (f *fakeRepo) ListByOwner(_ context.Context, _, ownerID uuid.UUID) ([]domain.TokenMeta, error) {
	var out []domain.TokenMeta
	for _, m := range f.byHash {
		if m.OwnerID == ownerID {
			out = append(out, m)
		}
	}
	return out, nil
}

func (f *fakeRepo) RevokeToken(_ context.Context, _, _, tokenID uuid.UUID) ([]byte, error) {
	hh, ok := f.hashByID[tokenID]
	if !ok {
		return nil, domain.ErrTokenNotFound
	}
	m := f.byHash[hh]
	now := time.Now()
	m.RevokedAt = &now
	f.byHash[hh] = m
	b, _ := hex.DecodeString(hh)
	return b, nil
}

func (f *fakeRepo) TouchLastUsed(_ context.Context, _, _ uuid.UUID) error {
	f.touched++
	return nil
}

type fakeCache struct {
	m map[string]domain.Identity
}

func newFakeCache() *fakeCache { return &fakeCache{m: map[string]domain.Identity{}} }

func (c *fakeCache) Get(_ context.Context, key string) (domain.Identity, bool, error) {
	id, ok := c.m[key]
	return id, ok, nil
}

func (c *fakeCache) Set(_ context.Context, key string, id domain.Identity, _ time.Duration) error {
	c.m[key] = id
	return nil
}

func (c *fakeCache) Delete(_ context.Context, key string) error {
	delete(c.m, key)
	return nil
}

func TestTokenIssuer_IssueAndAuthenticate(t *testing.T) {
	t.Parallel()
	// Arrange
	repo := newFakeRepo()
	cache := newFakeCache()
	issuer := apitoken.NewTokenIssuer(repo, cache, "ih_pat", time.Minute)
	owner := domain.Identity{UserID: uuid.New(), TenantID: uuid.New()}

	// Act
	plaintext, meta, err := issuer.IssueToken(context.Background(), owner, "ci", []string{domain.ScopeIntegrationsWrite}, nil)
	require.NoError(t, err)

	// Assert: plaintext shown once, prefixed; only hash stored.
	require.Contains(t, plaintext, "ih_pat_")
	require.Equal(t, owner.UserID, meta.OwnerID)
	require.Len(t, repo.byHash, 1)

	id, err := issuer.AuthenticateToken(context.Background(), plaintext)
	require.NoError(t, err)
	require.Equal(t, owner.UserID, id.UserID)
	require.Equal(t, owner.TenantID, id.TenantID)
	require.Equal(t, domain.AuthMethodToken, id.AuthMethod)
	require.True(t, id.HasScope(domain.ScopeIntegrationsWrite))
}

func TestTokenIssuer_AuthenticateToken_Errors(t *testing.T) {
	// Not parallel: subtests share the issuer/repo/cache and run sequentially.
	repo := newFakeRepo()
	cache := newFakeCache()
	issuer := apitoken.NewTokenIssuer(repo, cache, "ih_pat", time.Minute)
	owner := domain.Identity{UserID: uuid.New(), TenantID: uuid.New()}

	t.Run("unrecognized prefix", func(t *testing.T) {
		_, err := issuer.AuthenticateToken(context.Background(), "garbage")
		require.ErrorIs(t, err, domain.ErrTokenNotFound)
	})

	t.Run("expired", func(t *testing.T) {
		past := time.Now().Add(-time.Hour)
		plaintext, _, err := issuer.IssueToken(context.Background(), owner, "expired", nil, &past)
		require.NoError(t, err)
		_, err = issuer.AuthenticateToken(context.Background(), plaintext)
		require.ErrorIs(t, err, domain.ErrTokenExpired)
	})

	t.Run("cache hit on second auth", func(t *testing.T) {
		plaintext, _, err := issuer.IssueToken(context.Background(), owner, "cached", nil, nil)
		require.NoError(t, err)
		_, err = issuer.AuthenticateToken(context.Background(), plaintext)
		require.NoError(t, err)
		before := repo.touched
		// Second authentication should be served from cache (no extra TouchLastUsed).
		_, err = issuer.AuthenticateToken(context.Background(), plaintext)
		require.NoError(t, err)
		require.Equal(t, before, repo.touched, "second auth should hit the cache")
	})

	t.Run("list tokens", func(t *testing.T) {
		_, _, err := issuer.IssueToken(context.Background(), owner, "listed", nil, nil)
		require.NoError(t, err)
		list, err := issuer.ListTokens(context.Background(), owner)
		require.NoError(t, err)
		require.NotEmpty(t, list)
	})

	t.Run("revoked clears cache", func(t *testing.T) {
		// Isolated instances so the shared cache from sibling subtests doesn't
		// interfere with the "evicted" assertion.
		r, c := newFakeRepo(), newFakeCache()
		iss := apitoken.NewTokenIssuer(r, c, "ih_pat", time.Minute)
		plaintext, meta, err := iss.IssueToken(context.Background(), owner, "rev", nil, nil)
		require.NoError(t, err)
		// Prime cache.
		_, err = iss.AuthenticateToken(context.Background(), plaintext)
		require.NoError(t, err)
		require.NotEmpty(t, c.m)

		require.NoError(t, iss.RevokeToken(context.Background(), owner, meta.ID))
		require.Empty(t, c.m, "revoke must evict cache")

		_, err = iss.AuthenticateToken(context.Background(), plaintext)
		require.ErrorIs(t, err, domain.ErrTokenRevoked)
	})
}
