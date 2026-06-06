package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/config"
)

func validConfig() config.Config {
	return config.Config{
		HTTP:     config.HTTPConfig{Addr: ":8080"},
		Postgres: config.PostgresConfig{Host: "h", User: "u", DB: "d"},
		Redis:    config.RedisConfig{Addr: "r:6379"},
		Session:  config.SessionConfig{TTL: time.Hour, AbsoluteTTL: 24 * time.Hour},
		Crypto:   config.CryptoConfig{TokenKey: "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="},
		Argon2:   config.Argon2Config{Memory: 64 * 1024, Iterations: 3, Parallelism: 4, KeyLength: 32},
		APIToken: config.APITokenConfig{Prefix: "ih_pat"},
		Jira: config.JiraConfig{
			RedirectURI: "http://x/cb", Scopes: []string{"a"},
			AuthURL: "http://a", TokenURL: "http://t", APIBaseURL: "http://b",
		},
	}
}

func TestConfig_Validate(t *testing.T) {
	t.Parallel()
	base := validConfig()
	require.NoError(t, base.Validate())

	tests := []struct {
		name   string
		mutate func(*config.Config)
		want   string
	}{
		{"bad base64 key", func(c *config.Config) { c.Crypto.TokenKey = "!!!notbase64!!!" }, "base64"},
		{"wrong key length", func(c *config.Config) { c.Crypto.TokenKey = "c2hvcnQ=" }, "32 bytes"},
		{"absolute < ttl", func(c *config.Config) { c.Session.AbsoluteTTL = time.Minute }, "absolute_ttl"},
		{"argon2 memory too low", func(c *config.Config) { c.Argon2.Memory = 1 }, "argon2.memory"},
		{"missing redis", func(c *config.Config) { c.Redis.Addr = "" }, "redis.addr"},
		{"missing jira redirect", func(c *config.Config) { c.Jira.RedirectURI = "" }, "jira.redirect_uri"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := validConfig()
			tc.mutate(&c)
			err := c.Validate()
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestConfig_Helpers(t *testing.T) {
	t.Parallel()
	c := validConfig()
	require.False(t, c.IsProd())
	c.Env = "prod"
	require.True(t, c.IsProd())

	require.False(t, c.JiraConfigured())
	c.Jira.ClientID, c.Jira.ClientSecret = "id", "secret"
	require.True(t, c.JiraConfigured())

	require.Equal(t, "postgres://u:p@h:5432/d?sslmode=disable",
		config.PostgresConfig{User: "u", Password: "p", Host: "h", Port: 5432, DB: "d", SSLMode: "disable"}.DSN())
}
