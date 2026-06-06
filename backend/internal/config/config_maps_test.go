package config_test

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/config"
)

// validKey is a base64-std-encoded 32-byte AES key (so Validate passes).
func validKey() string {
	return base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
}

// TestLoad_EnvMapsEverySection guards OUR env->Config mapping (the explicit
// v.Get* wiring in Load) and env precedence — NOT viper itself. If a field is
// added to Config but its mapping line is forgotten, the value silently stays
// zero; setting a distinctive env per section and asserting it lands catches that
// for the covered types (string, int, int32, bool, duration, and CSV slice).
func TestLoad_EnvMapsEverySection(t *testing.T) {
	env := map[string]string{
		"ENV":                    "prod",
		"HTTP_ADDR":              ":9999",
		"POSTGRES_HOST":          "pg.example",
		"POSTGRES_PORT":          "5544",
		"POSTGRES_USER":          "app_user",
		"POSTGRES_DB":            "app_db",
		"POSTGRES_MAX_CONNS":     "42",
		"REDIS_ADDR":             "redis.example:6380",
		"REDIS_DB":               "3",
		"SESSION_TTL":            "48h",
		"ARGON2_MEMORY":          "32768",
		"API_TOKEN_PREFIX":       "tt_pat",
		"RATELIMIT_LOGIN_MAX":    "7",
		"CRYPTO_TOKEN_KEY":       validKey(),
		"JIRA_CLIENT_ID":         "cid-123",
		"JIRA_REDIRECT_URI":      "https://app.example/cb",
		"JIRA_USE_PKCE":          "false",
		"JIRA_INACTIVITY_WINDOW": "720h",
		"JIRA_SCOPES":            "read:jira-work, write:jira-work",
	}
	for k, v := range env {
		t.Setenv(k, v)
	}

	cfg, err := config.Load("")
	require.NoError(t, err)

	require.Equal(t, "prod", cfg.Env)
	require.Equal(t, ":9999", cfg.HTTP.Addr)
	require.Equal(t, "pg.example", cfg.Postgres.Host)
	require.Equal(t, 5544, cfg.Postgres.Port)
	require.Equal(t, "app_user", cfg.Postgres.User)
	require.Equal(t, "app_db", cfg.Postgres.DB)
	require.Equal(t, int32(42), cfg.Postgres.MaxConns)
	require.Equal(t, "redis.example:6380", cfg.Redis.Addr)
	require.Equal(t, 3, cfg.Redis.DB)
	require.Equal(t, 48*time.Hour, cfg.Session.TTL)
	require.Equal(t, uint32(32768), cfg.Argon2.Memory)
	require.Equal(t, "tt_pat", cfg.APIToken.Prefix)
	require.Equal(t, 7, cfg.RateLimit.LoginMax)
	require.Equal(t, "cid-123", cfg.Jira.ClientID)
	require.Equal(t, "https://app.example/cb", cfg.Jira.RedirectURI)
	require.False(t, cfg.Jira.UsePKCE)
	require.Equal(t, 720*time.Hour, cfg.Jira.InactivityWindow)
	// CSV from env is split and trimmed into a slice (getStringSlice/splitCSV).
	require.Equal(t, []string{"read:jira-work", "write:jira-work"}, cfg.Jira.Scopes)
}
