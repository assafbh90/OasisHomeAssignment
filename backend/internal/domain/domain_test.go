package domain_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/domain"
)

func TestIdentity_BelongsToTenant(t *testing.T) {
	t.Parallel()
	tenant := uuid.New()
	other := uuid.New()

	tests := []struct {
		name string
		id   domain.Identity
		ask  uuid.UUID
		want bool
	}{
		{name: "same tenant", id: domain.Identity{TenantID: tenant}, ask: tenant, want: true},
		{name: "different tenant", id: domain.Identity{TenantID: tenant}, ask: other, want: false},
		{name: "nil identity tenant", id: domain.Identity{}, ask: uuid.Nil, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.id.BelongsToTenant(tc.ask))
		})
	}
}

func TestIdentity_IsSamePrincipal(t *testing.T) {
	t.Parallel()
	tenant, user := uuid.New(), uuid.New()
	id := domain.Identity{TenantID: tenant, UserID: user}

	tests := []struct {
		name             string
		tenantID, userID uuid.UUID
		want             bool
	}{
		{name: "same tenant and user", tenantID: tenant, userID: user, want: true},
		{name: "different user", tenantID: tenant, userID: uuid.New(), want: false},
		{name: "different tenant", tenantID: uuid.New(), userID: user, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, id.IsSamePrincipal(tc.tenantID, tc.userID))
		})
	}
}

func TestIdentity_HasScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		id    domain.Identity
		scope string
		want  bool
	}{
		{
			name:  "token with scope",
			id:    domain.Identity{AuthMethod: domain.AuthMethodToken, Scopes: []string{domain.ScopeIntegrationsWrite}},
			scope: domain.ScopeIntegrationsWrite,
			want:  true,
		},
		{
			name:  "token without scope",
			id:    domain.Identity{AuthMethod: domain.AuthMethodToken, Scopes: []string{domain.ScopeIntegrationsRead}},
			scope: domain.ScopeIntegrationsWrite,
			want:  false,
		},
		{
			name:  "session is full-scope",
			id:    domain.Identity{AuthMethod: domain.AuthMethodSession},
			scope: domain.ScopeIntegrationsWrite,
			want:  true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, tc.id.HasScope(tc.scope))
		})
	}
}

func TestCredential_IsAccessTokenExpired(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	skew := time.Minute

	tests := []struct {
		name      string
		expiresAt time.Time
		want      bool
	}{
		{name: "valid, well in future", expiresAt: now.Add(time.Hour), want: false},
		{name: "expired", expiresAt: now.Add(-time.Hour), want: true},
		{name: "within skew counts as expired", expiresAt: now.Add(30 * time.Second), want: true},
		{name: "exactly at skew boundary counts as expired", expiresAt: now.Add(skew), want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &domain.Credential{AccessExpiresAt: tc.expiresAt}
			require.Equal(t, tc.want, c.IsAccessTokenExpired(now, skew))
		})
	}
}

func TestCredential_HasRefreshTokenLikelyExpired(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	inactivity := 90 * 24 * time.Hour

	tests := []struct {
		name       string
		lastUsed   time.Time
		inactivity time.Duration
		want       bool
	}{
		{name: "recently used", lastUsed: now.Add(-time.Hour), inactivity: inactivity, want: false},
		{name: "long inactive", lastUsed: now.Add(-100 * 24 * time.Hour), inactivity: inactivity, want: true},
		{name: "disabled when inactivity is zero", lastUsed: now.Add(-100 * 24 * time.Hour), inactivity: 0, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &domain.Credential{RefreshLastUsedAt: tc.lastUsed}
			require.Equal(t, tc.want, c.HasRefreshTokenLikelyExpired(now, tc.inactivity))
		})
	}
}

func TestCredential_NeedsReauth(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		status domain.ConnectionStatus
		want   bool
	}{
		{name: "connected", status: domain.StatusConnected, want: false},
		{name: "needs reauth", status: domain.StatusNeedsReauth, want: true},
		{name: "revoked", status: domain.StatusRevoked, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := &domain.Credential{Status: tc.status}
			require.Equal(t, tc.want, c.NeedsReauth())
		})
	}
}

func TestTokenMeta_IsExpired(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	require.False(t, domain.TokenMeta{}.IsExpired(now), "no expiry never expires")
	require.True(t, domain.TokenMeta{ExpiresAt: &past}.IsExpired(now))
	require.False(t, domain.TokenMeta{ExpiresAt: &future}.IsExpired(now))
}

func TestTokenMeta_IsRevoked(t *testing.T) {
	t.Parallel()
	now := time.Now()
	require.False(t, domain.TokenMeta{}.IsRevoked())
	require.True(t, domain.TokenMeta{RevokedAt: &now}.IsRevoked())
}

func TestUser_IsActive(t *testing.T) {
	t.Parallel()
	require.True(t, domain.User{Status: domain.UserStatusActive}.IsActive())
	require.False(t, domain.User{Status: domain.UserStatusDisabled}.IsActive())
}
