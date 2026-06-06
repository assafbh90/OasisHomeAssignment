//go:build integration

package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/app"
	"github.com/assafbh/identityhub/internal/config"
	"github.com/assafbh/identityhub/internal/logging"
)

// TestApp_CompositionRoot exercises the real composition root (Wire/Seed/Handler/
// Serve/Close) against the test containers — the wiring that cmd/api delegates to.
func TestApp_CompositionRoot(t *testing.T) {
	truncateAll(t)
	ctx := context.Background()

	t.Setenv("POSTGRES_HOST", pgHost)
	t.Setenv("POSTGRES_PORT", strconv.Itoa(pgPort))
	t.Setenv("POSTGRES_USER", "identityhub_app")
	t.Setenv("POSTGRES_PASSWORD", appPass)
	t.Setenv("POSTGRES_DB", dbName)
	t.Setenv("POSTGRES_SSLMODE", "disable")
	t.Setenv("REDIS_ADDR", redisClient.Options().Addr)
	t.Setenv("REDIS_PASSWORD", redisClient.Options().Password)
	t.Setenv("CRYPTO_TOKEN_KEY", cryptoKeyB64)
	t.Setenv("JIRA_REDIRECT_URI", "http://localhost:3000/v1/integrations/jira/callback")
	t.Setenv("HTTP_ADDR", "127.0.0.1:18098")
	t.Setenv("PPROF_ENABLED", "false")

	cfg, err := config.Load("")
	require.NoError(t, err)

	a, err := app.Build(ctx, cfg, logging.New("dev", "error"))
	require.NoError(t, err)
	defer a.Close()

	// Seeding is idempotent; run it twice.
	require.NoError(t, a.Seed(ctx))
	require.NoError(t, a.Seed(ctx))

	// The wired handler authenticates the seeded user.
	srv := httptest.NewServer(a.Handler())
	defer srv.Close()
	c := newClient(t, srv)
	rec := c.do(http.MethodPost, "/v1/auth/login", "", loginBody("admin@acme.test", "password123"))
	require.Equal(t, http.StatusOK, rec.Code(), "seeded user should authenticate")

	// Serve lifecycle: start, hit /healthz, then graceful shutdown via cancel.
	serveCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- a.Serve(serveCtx) }()

	var resp *http.Response
	for i := 0; i < 50; i++ {
		resp, err = http.Get("http://127.0.0.1:18098/healthz")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_ = resp.Body.Close()

	cancel()
	require.NoError(t, <-done)
}
