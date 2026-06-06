//go:build integration

package integration

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/config"
	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/integration/oauthtoken"
	"github.com/assafbh/identityhub/internal/logging"
	"github.com/assafbh/identityhub/internal/platform"
	"github.com/assafbh/identityhub/internal/session"
	store "github.com/assafbh/identityhub/internal/storage/postgres"
	redisstore "github.com/assafbh/identityhub/internal/storage/redis"
)

func truncateAll(t *testing.T) {
	t.Helper()
	_, err := adminPool.Exec(context.Background(),
		`TRUNCATE tenants, users, api_tokens, integration_credentials RESTART IDENTITY CASCADE`)
	require.NoError(t, err)
	require.NoError(t, redisClient.FlushDB(context.Background()).Err())
}

func seedTenantUser(t *testing.T) (tenantID, userID uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	tr := store.NewPostgresTenantRepository(appPool)
	ur := store.NewPostgresUserRepository(appPool)
	tenant, err := tr.Create(ctx, "acme", "Acme")
	require.NoError(t, err)
	user, err := ur.CreateUser(ctx, tenant.ID, "u@acme.test", "hash")
	require.NoError(t, err)
	return tenant.ID, user.ID
}

func TestUserRepository_FindUserForLogin(t *testing.T) {
	// The login lookup is global (no tenant context) via the SECURITY DEFINER
	// function, and must resolve the user's tenant. This is our auth-bootstrap
	// logic, so it's worth covering against the real DB + RLS setup.
	truncateAll(t)
	ctx := context.Background()
	tenantID, userID := seedTenantUser(t)
	ur := store.NewPostgresUserRepository(appPool)

	got, err := ur.FindUserForLogin(ctx, "u@acme.test")
	require.NoError(t, err)
	require.Equal(t, userID, got.ID)
	require.Equal(t, tenantID, got.TenantID, "login lookup resolves the tenant")

	_, err = ur.FindUserForLogin(ctx, "ghost@acme.test")
	require.ErrorIs(t, err, domain.ErrUserNotFound)
}

func TestApiTokenRepository_ListAndTouch(t *testing.T) {
	truncateAll(t)
	ctx := context.Background()
	tenantID, userID := seedTenantUser(t)
	repo := store.NewPostgresApiTokenRepository(appPool)

	meta := &domain.TokenMeta{TenantID: tenantID, OwnerID: userID, Name: "ci", Prefix: "ih_pat", Scopes: []string{"integrations:write"}}
	require.NoError(t, repo.SaveToken(ctx, meta, []byte("hash-bytes-1")))

	list, err := repo.ListByOwner(ctx, tenantID, userID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.NoError(t, repo.TouchLastUsed(ctx, tenantID, meta.ID))

	found, err := repo.FindByHash(ctx, []byte("hash-bytes-1"))
	require.NoError(t, err)
	require.Equal(t, meta.ID, found.ID)

	_, err = repo.RevokeToken(ctx, tenantID, userID, meta.ID)
	require.NoError(t, err)
	_, err = repo.RevokeToken(ctx, tenantID, userID, meta.ID) // already revoked
	require.ErrorIs(t, err, domain.ErrTokenNotFound)
}

func TestCredentialRepository_Lifecycle(t *testing.T) {
	truncateAll(t)
	ctx := context.Background()
	tenantID, userID := seedTenantUser(t)

	cipher, err := oauthtoken.NewAESGCMTokenCipher(mustKey(t))
	require.NoError(t, err)
	repo := store.NewPostgresCredentialRepository(appPool, cipher)

	cred := &domain.Credential{
		TenantID: tenantID, UserID: userID, Provider: domain.ProviderJira,
		AccessToken: "at", RefreshToken: "rt", Scopes: []string{"read:jira-work"},
		ExternalAccountID: "cloud-1", SiteURL: "https://acme.atlassian.net",
		AccessExpiresAt: time.Now().Add(time.Hour), RefreshLastUsedAt: time.Now(), Status: domain.StatusConnected,
	}
	require.NoError(t, repo.SaveCredential(ctx, cred))

	loaded, err := repo.LoadCredential(ctx, tenantID, userID, domain.ProviderJira)
	require.NoError(t, err)
	require.Equal(t, "rt", loaded.RefreshToken) // decrypted round-trip

	loaded.AccessToken = "at2"
	loaded.RefreshToken = "rt2"
	require.NoError(t, repo.UpdateTokens(ctx, loaded))
	again, err := repo.LoadCredential(ctx, tenantID, userID, domain.ProviderJira)
	require.NoError(t, err)
	require.Equal(t, "rt2", again.RefreshToken)

	require.NoError(t, repo.MarkNeedsReauth(ctx, tenantID, userID, domain.ProviderJira))
	again, _ = repo.LoadCredential(ctx, tenantID, userID, domain.ProviderJira)
	require.Equal(t, domain.StatusNeedsReauth, again.Status)

	require.NoError(t, repo.DeleteCredential(ctx, tenantID, userID, domain.ProviderJira))
	_, err = repo.LoadCredential(ctx, tenantID, userID, domain.ProviderJira)
	require.ErrorIs(t, err, domain.ErrCredentialNotFound)
}

func TestRedisStores(t *testing.T) {
	truncateAll(t)
	ctx := context.Background()
	tenantID, userID := uuid.New(), uuid.New()

	t.Run("session store", func(t *testing.T) {
		s := redisstore.NewRedisSessionStore(redisClient)
		data := session.SessionData{UserID: userID, TenantID: tenantID, CreatedAt: time.Now(), AbsoluteExpiresAt: time.Now().Add(time.Hour)}
		require.NoError(t, s.Save(ctx, "sid1", data, time.Hour))
		got, err := s.Find(ctx, "sid1")
		require.NoError(t, err)
		require.Equal(t, userID, got.UserID)
		require.NoError(t, s.Refresh(ctx, "sid1", time.Hour))
		require.NoError(t, s.DeleteAllForUser(ctx, userID))
		_, err = s.Find(ctx, "sid1")
		require.ErrorIs(t, err, domain.ErrSessionNotFound)
		// Refreshing a missing session reports not-found.
		require.ErrorIs(t, s.Refresh(ctx, "sid1", time.Hour), domain.ErrSessionNotFound)
	})

	t.Run("token cache", func(t *testing.T) {
		c := redisstore.NewRedisTokenCache(redisClient)
		id := domain.Identity{UserID: userID, TenantID: tenantID, Scopes: []string{"integrations:read"}, AuthMethod: domain.AuthMethodToken}
		require.NoError(t, c.Set(ctx, "k1", id, time.Minute))
		got, ok, err := c.Get(ctx, "k1")
		require.NoError(t, err)
		require.True(t, ok)
		require.Equal(t, userID, got.UserID)
		require.NoError(t, c.Delete(ctx, "k1"))
		_, ok, _ = c.Get(ctx, "k1")
		require.False(t, ok)
	})

	t.Run("rate limiter", func(t *testing.T) {
		rl := redisstore.NewRedisRateLimiter(redisClient, 2, time.Minute)
		ok, _, _ := rl.AllowAttempt(ctx, "ip:1")
		require.True(t, ok)
		ok, _, _ = rl.AllowAttempt(ctx, "ip:1")
		require.True(t, ok)
		ok, retry, _ := rl.AllowAttempt(ctx, "ip:1")
		require.False(t, ok)
		require.Positive(t, retry)
	})

	t.Run("oauth state one-time", func(t *testing.T) {
		ss := redisstore.NewRedisOAuthStateStore(redisClient, time.Minute)
		state, err := ss.GenerateState(ctx, tenantID, userID, "verifier")
		require.NoError(t, err)
		tID, uID, v, err := ss.ConsumeState(ctx, state)
		require.NoError(t, err)
		require.Equal(t, tenantID, tID)
		require.Equal(t, userID, uID)
		require.Equal(t, "verifier", v)
		_, _, _, err = ss.ConsumeState(ctx, state) // second use fails
		require.ErrorIs(t, err, domain.ErrStateNotFound)
	})

	t.Run("ticket cache", func(t *testing.T) {
		cache := redisstore.NewRedisTicketCache(redisClient, time.Minute)
		tid := uuid.New()
		require.NoError(t, cache.Replace(ctx, tid, []domain.CreatedTicket{
			{TenantID: tid, Provider: "jira", ProjectKey: "NHI", IssueKey: "NHI-1", CreatedAt: time.Now()},
			{TenantID: tid, Provider: "jira", ProjectKey: "OPS", IssueKey: "OPS-1", CreatedAt: time.Now()},
		}))
		got, err := cache.ListByProject(ctx, tid, "NHI", 10)
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Equal(t, "NHI-1", got[0].IssueKey)

		require.NoError(t, cache.Add(ctx, tid, domain.CreatedTicket{ProjectKey: "NHI", IssueKey: "NHI-2", CreatedAt: time.Now()}))
		got, _ = cache.ListByProject(ctx, tid, "NHI", 10)
		require.Len(t, got, 2)
	})

	t.Run("reconcile gate single-flight + throttle", func(t *testing.T) {
		gate := redisstore.NewRedisReconcileGate(redisClient, time.Minute)
		tid := uuid.New()

		ok1, finish1, err := gate.Begin(ctx, tid, false)
		require.NoError(t, err)
		require.True(t, ok1, "first caller proceeds")

		ok2, _, err := gate.Begin(ctx, tid, false)
		require.NoError(t, err)
		require.False(t, ok2, "concurrent caller is locked out (single-flight)")

		finish1() // release lock + stamp throttle

		ok3, _, _ := gate.Begin(ctx, tid, false)
		require.False(t, ok3, "throttled within the window")

		ok4, finish4, _ := gate.Begin(ctx, tid, true)
		require.True(t, ok4, "force bypasses the throttle")
		finish4()
	})
}

func TestPlatformServer_Lifecycle(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	// Enable pprof on a separate internal port so its mux/goroutine are exercised.
	srv := platform.NewServer(logging.New("dev", "error"),
		config.HTTPConfig{Addr: "127.0.0.1:18099", ShutdownTimeout: 2 * time.Second},
		config.PprofConfig{Enabled: true, Addr: "127.0.0.1:16060"}, mux)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	// Poll until the server is accepting connections.
	var resp *http.Response
	var err error
	for i := 0; i < 50; i++ {
		resp, err = http.Get("http://127.0.0.1:18099/healthz")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	// Hit the internal pprof endpoint to cover the pprof mux.
	if pr, perr := http.Get("http://127.0.0.1:16060/debug/pprof/"); perr == nil {
		_ = pr.Body.Close()
	}

	cancel() // triggers graceful shutdown
	require.NoError(t, <-done)
}

func mustKey(t *testing.T) []byte {
	t.Helper()
	k, err := config.CryptoConfig{TokenKey: cryptoKeyB64}.DecodedTokenKey()
	require.NoError(t, err)
	return k
}
