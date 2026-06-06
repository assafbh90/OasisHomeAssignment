// Package config loads and validates application configuration.
//
// Precedence: built-in defaults -> optional config file -> environment
// variables (env always wins). Validation is fail-fast: Load returns an error
// describing every problem rather than letting a half-configured process start.
package config

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config is the fully-resolved application configuration.
type Config struct {
	Env        string           `mapstructure:"env"`
	HTTP       HTTPConfig       `mapstructure:"http"`
	Pprof      PprofConfig      `mapstructure:"pprof"`
	Postgres   PostgresConfig   `mapstructure:"postgres"`
	Redis      RedisConfig      `mapstructure:"redis"`
	Session    SessionConfig    `mapstructure:"session"`
	Argon2     Argon2Config     `mapstructure:"argon2"`
	Crypto     CryptoConfig     `mapstructure:"crypto"`
	APIToken   APITokenConfig   `mapstructure:"api_token"`
	RateLimit  RateLimitConfig  `mapstructure:"ratelimit"`
	Jira       JiraConfig       `mapstructure:"jira"`
	Ollama     OllamaConfig     `mapstructure:"ollama"`
	Scheduler  SchedulerConfig  `mapstructure:"scheduler"`
	Automation AutomationConfig `mapstructure:"automation"`
}

// HTTPConfig configures the public HTTP server.
type HTTPConfig struct {
	Addr            string        `mapstructure:"addr"`
	ReadTimeout     time.Duration `mapstructure:"read_timeout"`
	WriteTimeout    time.Duration `mapstructure:"write_timeout"`
	IdleTimeout     time.Duration `mapstructure:"idle_timeout"`
	ShutdownTimeout time.Duration `mapstructure:"shutdown_timeout"`
	// AllowedOrigins is consulted only when the SPA is served cross-origin
	// (the default deployment proxies same-origin, so this can be empty).
	AllowedOrigins []string `mapstructure:"allowed_origins"`
}

// PprofConfig configures the internal-only profiling server.
type PprofConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Addr    string `mapstructure:"addr"`
}

// PostgresConfig configures the Postgres connection pool.
type PostgresConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	DB       string `mapstructure:"db"`
	SSLMode  string `mapstructure:"sslmode"`
	MaxConns int32  `mapstructure:"max_conns"`
	// StatementTimeout / IdleInTxTimeout are set as session GUCs on every
	// pooled connection: they bound query duration and reap transactions left
	// open by a crashed instance — general crash-safety hygiene for the pool.
	StatementTimeout time.Duration `mapstructure:"statement_timeout"`
	IdleInTxTimeout  time.Duration `mapstructure:"idle_in_tx_timeout"`
}

// DSN renders the pgx connection string.
func (p PostgresConfig) DSN() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		p.User, p.Password, p.Host, p.Port, p.DB, p.SSLMode)
}

// RedisConfig configures the Redis client.
type RedisConfig struct {
	Addr     string `mapstructure:"addr"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

// SessionConfig configures opaque server-side sessions.
type SessionConfig struct {
	TTL          time.Duration `mapstructure:"ttl"`          // sliding TTL refreshed on activity
	AbsoluteTTL  time.Duration `mapstructure:"absolute_ttl"` // hard maximum lifetime
	CookieName   string        `mapstructure:"cookie_name"`
	CookieSecure bool          `mapstructure:"cookie_secure"`
	CookieDomain string        `mapstructure:"cookie_domain"`
}

// Argon2Config configures the Argon2id password hasher.
type Argon2Config struct {
	Memory      uint32 `mapstructure:"memory"` // KiB
	Iterations  uint32 `mapstructure:"iterations"`
	Parallelism uint8  `mapstructure:"parallelism"`
	SaltLength  uint32 `mapstructure:"salt_length"`
	KeyLength   uint32 `mapstructure:"key_length"`
}

// CryptoConfig holds the symmetric key for encrypting provider tokens at rest.
type CryptoConfig struct {
	// TokenKey is a base64-std-encoded 32-byte key for AES-256-GCM.
	TokenKey string `mapstructure:"token_key"`
}

// DecodedTokenKey returns the raw 32-byte key.
func (c CryptoConfig) DecodedTokenKey() ([]byte, error) {
	return base64.StdEncoding.DecodeString(c.TokenKey)
}

// APITokenConfig configures machine API keys (PATs).
type APITokenConfig struct {
	Prefix   string        `mapstructure:"prefix"`
	CacheTTL time.Duration `mapstructure:"cache_ttl"`
}

// RateLimitConfig configures the login/oauth rate limiter.
type RateLimitConfig struct {
	LoginMax    int           `mapstructure:"login_max"`
	LoginWindow time.Duration `mapstructure:"login_window"`
}

// JiraConfig configures the Jira Cloud 3LO provider.
type JiraConfig struct {
	ClientID         string        `mapstructure:"client_id"`
	ClientSecret     string        `mapstructure:"client_secret"`
	RedirectURI      string        `mapstructure:"redirect_uri"`
	Scopes           []string      `mapstructure:"scopes"`
	AuthURL          string        `mapstructure:"auth_url"`
	TokenURL         string        `mapstructure:"token_url"`
	APIBaseURL       string        `mapstructure:"api_base_url"`
	UsePKCE          bool          `mapstructure:"use_pkce"`
	InactivityWindow time.Duration `mapstructure:"inactivity_window"`
	AccessTokenSkew  time.Duration `mapstructure:"access_token_skew"`
	HTTPTimeout      time.Duration `mapstructure:"http_timeout"`
}

// OllamaConfig configures the local LLM used to summarize blog posts.
type OllamaConfig struct {
	BaseURL       string        `mapstructure:"base_url"`
	Model         string        `mapstructure:"model"`
	Timeout       time.Duration `mapstructure:"timeout"`
	MaxInputChars int           `mapstructure:"max_input_chars"`
}

// SchedulerConfig configures the automation scheduler worker.
type SchedulerConfig struct {
	Tick       time.Duration `mapstructure:"tick"`        // how often to poll for due automations
	ClaimBatch int           `mapstructure:"claim_batch"` // max automations claimed per tick
	Lease      time.Duration `mapstructure:"lease"`       // a running row older than this is reclaimable (crash self-heal)
}

// AutomationConfig configures a single automation run.
type AutomationConfig struct {
	MaxPostsPerRun  int           `mapstructure:"max_posts_per_run"` // cap per run; 0 = unlimited
	DefaultInterval time.Duration `mapstructure:"default_interval"`  // steady-state scan interval once caught up
	DrainInterval   time.Duration `mapstructure:"drain_interval"`    // short reschedule while a backlog remains (fast initial drain)
	HTTPTimeout     time.Duration `mapstructure:"http_timeout"`      // timeout for sitemap/scrape fetches
}

// IsProd reports whether the process runs in production mode.
func (c Config) IsProd() bool { return strings.EqualFold(c.Env, "prod") }

// JiraConfigured reports whether the Jira OAuth client credentials are present.
// When false, the stack still runs but the integration cannot complete OAuth.
func (c Config) JiraConfigured() bool {
	return c.Jira.ClientID != "" && c.Jira.ClientSecret != ""
}

// Load resolves configuration from defaults, an optional file at path (may be
// empty), and environment variables, then validates it.
func Load(path string) (Config, error) {
	v := viper.New()
	setDefaults(v)

	if path != "" {
		v.SetConfigFile(path)
		if err := v.ReadInConfig(); err != nil {
			return Config{}, fmt.Errorf("read config file %q: %w", path, err)
		}
	}

	// Env wins. Map nested keys (postgres.host) to env (POSTGRES_HOST). We read
	// values via explicit v.Get* below rather than v.Unmarshal, because viper's
	// Unmarshal does not honor AutomaticEnv (a well-known gotcha) — Get* does.
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	cfg := Config{
		Env: v.GetString(keyEnv),
		HTTP: HTTPConfig{
			Addr:            v.GetString(keyHTTPAddr),
			ReadTimeout:     v.GetDuration(keyHTTPReadTimeout),
			WriteTimeout:    v.GetDuration(keyHTTPWriteTimeout),
			IdleTimeout:     v.GetDuration(keyHTTPIdleTimeout),
			ShutdownTimeout: v.GetDuration(keyHTTPShutdownTimeout),
			AllowedOrigins:  getStringSlice(v, keyHTTPAllowedOrigins),
		},
		Pprof: PprofConfig{
			Enabled: v.GetBool(keyPprofEnabled),
			Addr:    v.GetString(keyPprofAddr),
		},
		Postgres: PostgresConfig{
			Host:             v.GetString(keyPostgresHost),
			Port:             v.GetInt(keyPostgresPort),
			User:             v.GetString(keyPostgresUser),
			Password:         v.GetString(keyPostgresPassword),
			DB:               v.GetString(keyPostgresDB),
			SSLMode:          v.GetString(keyPostgresSSLMode),
			MaxConns:         v.GetInt32(keyPostgresMaxConns),
			StatementTimeout: v.GetDuration(keyPostgresStatementTimeout),
			IdleInTxTimeout:  v.GetDuration(keyPostgresIdleInTxTimeout),
		},
		Redis: RedisConfig{
			Addr:     v.GetString(keyRedisAddr),
			Password: v.GetString(keyRedisPassword),
			DB:       v.GetInt(keyRedisDB),
		},
		Session: SessionConfig{
			TTL:          v.GetDuration(keySessionTTL),
			AbsoluteTTL:  v.GetDuration(keySessionAbsoluteTTL),
			CookieName:   v.GetString(keySessionCookieName),
			CookieSecure: v.GetBool(keySessionCookieSecure),
			CookieDomain: v.GetString(keySessionCookieDomain),
		},
		Argon2: Argon2Config{
			Memory:      v.GetUint32(keyArgon2Memory),
			Iterations:  v.GetUint32(keyArgon2Iterations),
			Parallelism: uint8(v.GetUint(keyArgon2Parallelism)),
			SaltLength:  v.GetUint32(keyArgon2SaltLength),
			KeyLength:   v.GetUint32(keyArgon2KeyLength),
		},
		Crypto: CryptoConfig{TokenKey: v.GetString(keyCryptoTokenKey)},
		APIToken: APITokenConfig{
			Prefix:   v.GetString(keyAPITokenPrefix),
			CacheTTL: v.GetDuration(keyAPITokenCacheTTL),
		},
		RateLimit: RateLimitConfig{
			LoginMax:    v.GetInt(keyRateLimitLoginMax),
			LoginWindow: v.GetDuration(keyRateLimitLoginWindow),
		},
		Jira: JiraConfig{
			ClientID:         v.GetString(keyJiraClientID),
			ClientSecret:     v.GetString(keyJiraClientSecret),
			RedirectURI:      v.GetString(keyJiraRedirectURI),
			Scopes:           getStringSlice(v, keyJiraScopes),
			AuthURL:          v.GetString(keyJiraAuthURL),
			TokenURL:         v.GetString(keyJiraTokenURL),
			APIBaseURL:       v.GetString(keyJiraAPIBaseURL),
			UsePKCE:          v.GetBool(keyJiraUsePKCE),
			InactivityWindow: v.GetDuration(keyJiraInactivityWindow),
			AccessTokenSkew:  v.GetDuration(keyJiraAccessTokenSkew),
			HTTPTimeout:      v.GetDuration(keyJiraHTTPTimeout),
		},
		Ollama: OllamaConfig{
			BaseURL:       v.GetString(keyOllamaBaseURL),
			Model:         v.GetString(keyOllamaModel),
			Timeout:       v.GetDuration(keyOllamaTimeout),
			MaxInputChars: v.GetInt(keyOllamaMaxInputChars),
		},
		Scheduler: SchedulerConfig{
			Tick:       v.GetDuration(keySchedulerTick),
			ClaimBatch: v.GetInt(keySchedulerClaimBatch),
			Lease:      v.GetDuration(keySchedulerLease),
		},
		Automation: AutomationConfig{
			MaxPostsPerRun:  v.GetInt(keyAutomationMaxPostsPerRun),
			DefaultInterval: v.GetDuration(keyAutomationDefaultInterval),
			DrainInterval:   v.GetDuration(keyAutomationDrainInterval),
			HTTPTimeout:     v.GetDuration(keyAutomationHTTPTimeout),
		},
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// getStringSlice reads a value that may arrive as a real []string (default/file)
// or a comma-separated string (env), normalizing to a trimmed slice.
func getStringSlice(v *viper.Viper, key string) []string {
	switch val := v.Get(key).(type) {
	case []string:
		return val
	case string:
		return splitCSV(val)
	case []any:
		out := make([]string, 0, len(val))
		for _, e := range val {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// Built-in default values (overridable via file/env).
const (
	defaultPostgresPort      = 5432
	defaultMaxConns          = 20
	defaultArgon2MemoryKiB   = 64 * 1024 // ~64 MiB
	defaultArgon2Iterations  = 3
	defaultArgon2Parallelism = 4
	defaultArgon2SaltLength  = 16
	defaultArgon2KeyLength   = 32
	defaultLoginMax          = 10

	// aesKeyBytes is the AES-256 key length; the min* values are the lowest
	// acceptable Argon2 parameters at validation time.
	aesKeyBytes        = 32
	minArgon2KeyLength = 16
	minArgon2MemoryKiB = 8 * 1024 // 8 MiB
)

func setDefaults(v *viper.Viper) {
	v.SetDefault(keyEnv, "dev")

	v.SetDefault(keyHTTPAddr, ":8080")
	v.SetDefault(keyHTTPReadTimeout, "15s")
	v.SetDefault(keyHTTPWriteTimeout, "30s")
	v.SetDefault(keyHTTPIdleTimeout, "60s")
	v.SetDefault(keyHTTPShutdownTimeout, "15s")
	v.SetDefault(keyHTTPAllowedOrigins, []string{})

	v.SetDefault(keyPprofEnabled, true)
	v.SetDefault(keyPprofAddr, "127.0.0.1:6060")

	v.SetDefault(keyPostgresHost, "localhost")
	v.SetDefault(keyPostgresPort, defaultPostgresPort)
	v.SetDefault(keyPostgresSSLMode, "disable")
	v.SetDefault(keyPostgresMaxConns, defaultMaxConns)
	v.SetDefault(keyPostgresStatementTimeout, "30s")
	v.SetDefault(keyPostgresIdleInTxTimeout, "15s")

	v.SetDefault(keyRedisAddr, "localhost:6379")
	v.SetDefault(keyRedisDB, 0)

	v.SetDefault(keySessionTTL, "24h")
	v.SetDefault(keySessionAbsoluteTTL, "168h")
	v.SetDefault(keySessionCookieName, "ih_session")
	v.SetDefault(keySessionCookieSecure, false)

	// OWASP-ish Argon2id defaults: ~64 MiB, 3 iterations, parallelism 4.
	v.SetDefault(keyArgon2Memory, defaultArgon2MemoryKiB)
	v.SetDefault(keyArgon2Iterations, defaultArgon2Iterations)
	v.SetDefault(keyArgon2Parallelism, defaultArgon2Parallelism)
	v.SetDefault(keyArgon2SaltLength, defaultArgon2SaltLength)
	v.SetDefault(keyArgon2KeyLength, defaultArgon2KeyLength)

	v.SetDefault(keyAPITokenPrefix, "ih_pat")
	v.SetDefault(keyAPITokenCacheTTL, "60s")

	v.SetDefault(keyRateLimitLoginMax, defaultLoginMax)
	v.SetDefault(keyRateLimitLoginWindow, "1m")

	v.SetDefault(keyJiraScopes, []string{"read:jira-work", "write:jira-work", "read:jira-user", "offline_access"})
	v.SetDefault(keyJiraAuthURL, "https://auth.atlassian.com/authorize")
	v.SetDefault(keyJiraTokenURL, "https://auth.atlassian.com/oauth/token")
	v.SetDefault(keyJiraAPIBaseURL, "https://api.atlassian.com")
	v.SetDefault(keyJiraUsePKCE, true)
	v.SetDefault(keyJiraInactivityWindow, "2160h") // 90 days
	v.SetDefault(keyJiraAccessTokenSkew, "60s")
	v.SetDefault(keyJiraHTTPTimeout, "10s")

	v.SetDefault(keyOllamaBaseURL, "http://ollama:11434")
	v.SetDefault(keyOllamaModel, "qwen2.5:0.5b")
	v.SetDefault(keyOllamaTimeout, "120s")
	v.SetDefault(keyOllamaMaxInputChars, 8000)

	v.SetDefault(keySchedulerTick, "10s")
	v.SetDefault(keySchedulerClaimBatch, 5)
	v.SetDefault(keySchedulerLease, "10m")

	v.SetDefault(keyAutomationMaxPostsPerRun, 5)
	v.SetDefault(keyAutomationDefaultInterval, "1h")
	v.SetDefault(keyAutomationDrainInterval, "15s")
	v.SetDefault(keyAutomationHTTPTimeout, "15s")
}

// Validate enforces required fields and structural invariants.
func (c *Config) Validate() error {
	var errs []string
	req := func(cond bool, msg string) {
		if !cond {
			errs = append(errs, msg)
		}
	}

	req(c.HTTP.Addr != "", "http.addr is required")
	req(c.Postgres.Host != "" && c.Postgres.User != "" && c.Postgres.DB != "",
		"postgres host/user/db are required")
	req(c.Redis.Addr != "", "redis.addr is required")

	req(c.Session.TTL > 0, "session.ttl must be > 0")
	req(c.Session.AbsoluteTTL >= c.Session.TTL, "session.absolute_ttl must be >= session.ttl")

	if key, err := c.Crypto.DecodedTokenKey(); err != nil {
		errs = append(errs, "crypto.token_key must be base64-encoded")
	} else if len(key) != aesKeyBytes {
		errs = append(errs, fmt.Sprintf("crypto.token_key must decode to %d bytes, got %d", aesKeyBytes, len(key)))
	}

	req(c.Argon2.Memory >= minArgon2MemoryKiB, "argon2.memory too low (>= 8 MiB)")
	req(c.Argon2.Iterations >= 1, "argon2.iterations must be >= 1")
	req(c.Argon2.Parallelism >= 1, "argon2.parallelism must be >= 1")
	req(c.Argon2.KeyLength >= minArgon2KeyLength, "argon2.key_length must be >= 16")

	req(c.APIToken.Prefix != "", "api_token.prefix is required")

	// Jira endpoint config has defaults, so we require it structurally. The
	// client_id/secret are NOT fatal when missing — the stack still boots so auth
	// and the UI are usable; main logs a warning and the integration endpoints
	// return a clear error until configured (see Config.JiraConfigured).
	req(c.Jira.RedirectURI != "", "jira.redirect_uri is required")
	req(len(c.Jira.Scopes) > 0, "jira.scopes must not be empty")
	req(c.Jira.AuthURL != "" && c.Jira.TokenURL != "" && c.Jira.APIBaseURL != "",
		"jira auth_url/token_url/api_base_url are required")

	if len(errs) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}
