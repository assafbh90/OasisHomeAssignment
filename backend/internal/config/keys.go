package config

// Centralized configuration keys and bootstrap environment variable names. Each
// viper key is used in two places (setDefaults + Load); naming them once keeps
// the two in lockstep and makes typos impossible.

// Bootstrap environment variables read directly (outside the viper key space).
const (
	EnvConfigFile = "CONFIG_FILE" // optional path to a config file
	EnvLogLevel   = "LOG_LEVEL"   // debug|info|warn|error
)

// Viper configuration keys. Their env-var form is the UPPER_SNAKE of the key
// (dots → underscores), e.g. keyPostgresHost -> POSTGRES_HOST.
const (
	keyEnv = "env"

	keyHTTPAddr            = "http.addr"
	keyHTTPReadTimeout     = "http.read_timeout"
	keyHTTPWriteTimeout    = "http.write_timeout"
	keyHTTPIdleTimeout     = "http.idle_timeout"
	keyHTTPShutdownTimeout = "http.shutdown_timeout"
	keyHTTPAllowedOrigins  = "http.allowed_origins"

	keyPprofEnabled = "pprof.enabled"
	keyPprofAddr    = "pprof.addr"

	keyPostgresHost             = "postgres.host"
	keyPostgresPort             = "postgres.port"
	keyPostgresUser             = "postgres.user"
	keyPostgresPassword         = "postgres.password"
	keyPostgresDB               = "postgres.db"
	keyPostgresSSLMode          = "postgres.sslmode"
	keyPostgresMaxConns         = "postgres.max_conns"
	keyPostgresStatementTimeout = "postgres.statement_timeout"
	keyPostgresIdleInTxTimeout  = "postgres.idle_in_tx_timeout"

	keyRedisAddr     = "redis.addr"
	keyRedisPassword = "redis.password"
	keyRedisDB       = "redis.db"

	keySessionTTL          = "session.ttl"
	keySessionAbsoluteTTL  = "session.absolute_ttl"
	keySessionCookieName   = "session.cookie_name"
	keySessionCookieSecure = "session.cookie_secure"
	keySessionCookieDomain = "session.cookie_domain"

	keyArgon2Memory      = "argon2.memory"
	keyArgon2Iterations  = "argon2.iterations"
	keyArgon2Parallelism = "argon2.parallelism"
	keyArgon2SaltLength  = "argon2.salt_length"
	keyArgon2KeyLength   = "argon2.key_length"

	keyCryptoTokenKey = "crypto.token_key"

	keyAPITokenPrefix   = "api_token.prefix"
	keyAPITokenCacheTTL = "api_token.cache_ttl"

	keyRateLimitLoginMax    = "ratelimit.login_max"
	keyRateLimitLoginWindow = "ratelimit.login_window"

	keyJiraClientID            = "jira.client_id"
	keyJiraClientSecret        = "jira.client_secret"
	keyJiraRedirectURI         = "jira.redirect_uri"
	keyJiraScopes              = "jira.scopes"
	keyJiraAuthURL             = "jira.auth_url"
	keyJiraTokenURL            = "jira.token_url"
	keyJiraAPIBaseURL          = "jira.api_base_url"
	keyJiraUsePKCE             = "jira.use_pkce"
	keyJiraRotatesRefreshToken = "jira.rotates_refresh_token"
	keyJiraInactivityWindow    = "jira.inactivity_window"
	keyJiraAbsoluteWindow      = "jira.absolute_window"
	keyJiraAccessTokenSkew     = "jira.access_token_skew"
	keyJiraHTTPTimeout         = "jira.http_timeout"
)
