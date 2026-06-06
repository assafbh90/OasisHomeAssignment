// Package app is the composition root: it wires concrete adapters to the
// consumer-defined interfaces and runs the server. Keeping this in a package
// (rather than package main) makes the wiring testable; cmd/api is a thin shell.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	redis "github.com/redis/go-redis/v9"

	"github.com/assafbh/identityhub/internal/apitoken"
	"github.com/assafbh/identityhub/internal/auth"
	"github.com/assafbh/identityhub/internal/config"
	"github.com/assafbh/identityhub/internal/integration"
	"github.com/assafbh/identityhub/internal/integration/client"
	"github.com/assafbh/identityhub/internal/integration/oauth"
	"github.com/assafbh/identityhub/internal/integration/oauthtoken"
	"github.com/assafbh/identityhub/internal/logging"
	"github.com/assafbh/identityhub/internal/platform"
	"github.com/assafbh/identityhub/internal/session"
	store "github.com/assafbh/identityhub/internal/storage/postgres"
	redisstore "github.com/assafbh/identityhub/internal/storage/redis"
	"github.com/assafbh/identityhub/internal/ticketreport"
	transport "github.com/assafbh/identityhub/internal/transport/http"
)

// CmdSeed is the subcommand that seeds demo data instead of serving.
const CmdSeed = "seed"

// oauthStateTTL bounds how long a user has to complete the OAuth consent.
const oauthStateTTL = 10 * time.Minute

// App holds the fully-wired dependencies shared by the server and subcommands.
type App struct {
	cfg     config.Config
	log     *slog.Logger
	deps    transport.RouterDeps
	tenants *store.PostgresTenantRepository
	users   *store.PostgresUserRepository
	hasher  *auth.Argon2PasswordHasher
	closers []func()
}

// Run is the process entrypoint: load config, build the logger, wire everything,
// then either seed or serve (until SIGINT/SIGTERM).
func Run() error {
	cfg, err := config.Load(os.Getenv(config.EnvConfigFile))
	if err != nil {
		return err
	}
	log := logging.New(cfg.Env, os.Getenv(config.EnvLogLevel))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	a, err := Wire(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer a.Close()

	if len(os.Args) > 1 && os.Args[1] == CmdSeed {
		return a.Seed(ctx)
	}
	return a.Serve(ctx)
}

// Wire builds and connects all adapters and services. It is the only place
// concretes are bound to interfaces; it reads top-down as infra → repos → redis
// → services → transport.
func Wire(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	a := &App{cfg: cfg, log: log}

	pool, redisClient, cipher, err := a.initInfra(ctx)
	if err != nil {
		return nil, err
	}
	repos := newRepos(pool, cipher)
	redisStores := newRedisAdapters(redisClient, cfg)
	a.tenants, a.users = repos.tenants, repos.users // also held on App for Seed

	// Auth services.
	a.hasher = auth.NewArgon2PasswordHasher(auth.Argon2Params{
		Memory: cfg.Argon2.Memory, Iterations: cfg.Argon2.Iterations, Parallelism: cfg.Argon2.Parallelism,
		SaltLength: cfg.Argon2.SaltLength, KeyLength: cfg.Argon2.KeyLength,
	})
	authenticator := auth.NewUserAuthenticator(a.users, a.hasher)
	sessionMgr := session.NewManager(redisStores.sessions, cfg.Session.TTL, cfg.Session.AbsoluteTTL)
	tokenIssuer := apitoken.NewTokenIssuer(repos.tokens, redisStores.tokens, cfg.APIToken.Prefix, cfg.APIToken.CacheTTL)

	// Jira integration.
	jiraProvider := oauth.NewJiraOAuthProvider(oauth.JiraConfig{
		ClientID: cfg.Jira.ClientID, ClientSecret: cfg.Jira.ClientSecret, RedirectURI: cfg.Jira.RedirectURI,
		Scopes: cfg.Jira.Scopes, AuthURL: cfg.Jira.AuthURL, TokenURL: cfg.Jira.TokenURL, APIBaseURL: cfg.Jira.APIBaseURL,
		UsePKCE: cfg.Jira.UsePKCE, RotatesRefreshToken: cfg.Jira.RotatesRefreshToken,
		InactivityWindow: cfg.Jira.InactivityWindow, HTTPTimeout: cfg.Jira.HTTPTimeout,
	})
	jiraClient := client.NewJiraClient(cfg.Jira.APIBaseURL, cfg.Jira.HTTPTimeout)
	tokenManager := oauthtoken.NewReactiveTokenManager(oauthtoken.Deps{
		Repo: repos.creds, Provider: jiraProvider, ProviderName: jiraProvider.Name(),
		Skew: cfg.Jira.AccessTokenSkew,
	})
	connectionSvc := integration.NewConnectionService(integration.Deps{
		Provider: jiraProvider, State: redisStores.state, Repo: repos.creds, UsePKCE: cfg.Jira.UsePKCE,
	})
	reportSvc := ticketreport.NewService(ticketreport.Deps{
		Provider: jiraProvider.Name(), Tokens: tokenManager, Creds: repos.creds,
		Client: jiraClient, Tickets: repos.tickets,
	})

	// Transport: identity resolvers (bearer first, then session), then handlers.
	resolver := transport.NewChainIdentityResolver(
		transport.NewBearerTokenResolver(tokenIssuer),
		transport.NewSessionIdentityResolver(sessionMgr, cfg.Session.CookieName),
	)
	cookie := transport.CookieConfig{
		SessionName: cfg.Session.CookieName, Secure: cfg.Session.CookieSecure,
		Domain: cfg.Session.CookieDomain, MaxAge: cfg.Session.TTL,
	}
	a.deps = transport.RouterDeps{
		Logger:      log,
		Resolver:    resolver,
		TLSEnabled:  cfg.Session.CookieSecure,
		AllowOrigin: cfg.HTTP.AllowedOrigins,
		Auth:        transport.NewAuthHandler(authenticator, sessionMgr, redisStores.rate, cookie),
		Tokens:      transport.NewTokenHandler(tokenIssuer),
		Health:      transport.NewHealthHandler(pool, platform.RedisPinger{Client: redisClient}),
		Integration: transport.NewIntegrationHandler(connectionSvc, reportSvc, "/"),
	}
	return a, nil
}

// initInfra opens the Postgres pool, Redis client, and token cipher, registering
// each for cleanup. These are the only fallible steps in wiring.
func (a *App) initInfra(ctx context.Context) (*pgxpool.Pool, *redis.Client, *oauthtoken.AESGCMTokenCipher, error) {
	pool, err := platform.NewPostgresPool(ctx, a.cfg.Postgres)
	if err != nil {
		return nil, nil, nil, err
	}
	a.closers = append(a.closers, pool.Close)

	redisClient, err := platform.NewRedisClient(ctx, a.cfg.Redis)
	if err != nil {
		return nil, nil, nil, err
	}
	a.closers = append(a.closers, func() { _ = redisClient.Close() })

	key, err := a.cfg.Crypto.DecodedTokenKey()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("decode crypto key: %w", err)
	}
	cipher, err := oauthtoken.NewAESGCMTokenCipher(key)
	if err != nil {
		return nil, nil, nil, err
	}
	return pool, redisClient, cipher, nil
}

// repos groups the Postgres repositories so Wire passes one value, not several.
type repos struct {
	tenants *store.PostgresTenantRepository
	users   *store.PostgresUserRepository
	tokens  *store.PostgresApiTokenRepository
	creds   *store.PostgresCredentialRepository
	tickets *store.PostgresTicketRepository
}

func newRepos(pool *pgxpool.Pool, cipher store.TokenCipher) repos {
	return repos{
		tenants: store.NewPostgresTenantRepository(pool),
		users:   store.NewPostgresUserRepository(pool),
		tokens:  store.NewPostgresApiTokenRepository(pool),
		creds:   store.NewPostgresCredentialRepository(pool, cipher),
		tickets: store.NewPostgresTicketRepository(pool),
	}
}

// redisAdapters groups the Redis-backed adapters.
type redisAdapters struct {
	sessions *redisstore.RedisSessionStore
	tokens   *redisstore.RedisTokenCache
	rate     *redisstore.RedisRateLimiter
	state    *redisstore.RedisOAuthStateStore
}

func newRedisAdapters(redisClient *redis.Client, cfg config.Config) redisAdapters {
	return redisAdapters{
		sessions: redisstore.NewRedisSessionStore(redisClient),
		tokens:   redisstore.NewRedisTokenCache(redisClient),
		rate:     redisstore.NewRedisRateLimiter(redisClient, cfg.RateLimit.LoginMax, cfg.RateLimit.LoginWindow),
		state:    redisstore.NewRedisOAuthStateStore(redisClient, oauthStateTTL),
	}
}

// Handler returns the wired HTTP handler (used by tests).
func (a *App) Handler() *gin.Engine { return transport.NewRouter(a.deps) }

// Serve starts the HTTP server (and pprof) and blocks until ctx is cancelled.
func (a *App) Serve(ctx context.Context) error {
	if a.cfg.IsProd() {
		gin.SetMode(gin.ReleaseMode)
	}
	srv := platform.NewServer(a.log, a.cfg.HTTP, a.cfg.Pprof, a.Handler())
	if !a.cfg.JiraConfigured() {
		a.log.Warn("Jira client credentials are not set — the integration cannot complete OAuth until JIRA_CLIENT_ID/JIRA_CLIENT_SECRET are configured")
	}
	a.log.Info("starting IdentityHub", slog.String("env", a.cfg.Env))
	return srv.Run(ctx)
}

// Close releases resources in reverse order of acquisition.
func (a *App) Close() {
	for i := len(a.closers) - 1; i >= 0; i-- {
		a.closers[i]()
	}
}
