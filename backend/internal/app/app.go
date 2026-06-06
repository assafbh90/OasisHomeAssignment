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
	"github.com/assafbh/identityhub/internal/automation"
	"github.com/assafbh/identityhub/internal/automation/discover"
	"github.com/assafbh/identityhub/internal/automation/scrape"
	"github.com/assafbh/identityhub/internal/automation/summarize"
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

// CmdScheduler runs the automation scheduler worker instead of the HTTP server.
const CmdScheduler = "scheduler"

const (
	// oauthStateTTL bounds how long a user has to complete the OAuth consent.
	oauthStateTTL = 10 * time.Minute
	// ticketCacheTTL bounds how long the tenant ticket cache lives before it must
	// be repopulated from Jira on the next connect/refresh.
	ticketCacheTTL = 24 * time.Hour
	// reconcileWindow is the minimum interval between (unforced) reconciles per tenant.
	reconcileWindow = 30 * time.Minute
)

// App holds the fully-wired dependencies shared by the server and subcommands.
type App struct {
	cfg      config.Config
	log      *slog.Logger
	deps     transport.RouterDeps
	tenants  *store.PostgresTenantRepository
	users    *store.PostgresUserRepository
	hasher   *auth.Argon2PasswordHasher
	autoSvc  *automation.Service
	autoRepo *store.PostgresAutomationRepository
	closers  []func()
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

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case CmdSeed:
			return a.Seed(ctx)
		case CmdScheduler:
			return a.RunScheduler(ctx)
		}
	}
	return a.Serve(ctx)
}

// Wire builds and connects all adapters and services. It is the only place
// concretes are bound to interfaces. The body is a table of contents — each step
// builds one layer and hands its outputs to the next: infra → repos → redis →
// auth → Jira → automation → transport.
func Wire(ctx context.Context, cfg config.Config, log *slog.Logger) (*App, error) {
	a := &App{cfg: cfg, log: log}

	pool, redisClient, cipher, err := a.initInfra(ctx)
	if err != nil {
		return nil, err
	}
	repos := newRepos(pool, cipher)
	redisStores := newRedisAdapters(redisClient, cfg)
	a.tenants, a.users = repos.tenants, repos.users // also held on App for Seed

	authn := a.buildAuth(repos, redisStores)
	jira := a.buildJiraIntegration(repos, redisStores)
	a.buildAutomation(pool, redisClient, jira.reports)
	a.deps = a.buildRouterDeps(pool, redisClient, redisStores, authn, jira)

	return a, nil
}

// authServices are the authentication components the transport layer consumes.
type authServices struct {
	authenticator *auth.UserAuthenticator
	sessions      *session.Manager
	tokens        *apitoken.TokenIssuer
}

// buildAuth wires password authentication, opaque sessions, and machine API keys.
func (a *App) buildAuth(repos repos, redisStores redisAdapters) authServices {
	a.hasher = auth.NewArgon2PasswordHasher(auth.Argon2Params{
		Memory: a.cfg.Argon2.Memory, Iterations: a.cfg.Argon2.Iterations, Parallelism: a.cfg.Argon2.Parallelism,
		SaltLength: a.cfg.Argon2.SaltLength, KeyLength: a.cfg.Argon2.KeyLength,
	})
	return authServices{
		authenticator: auth.NewUserAuthenticator(a.users, a.hasher),
		sessions:      session.NewManager(redisStores.sessions, a.cfg.Session.TTL, a.cfg.Session.AbsoluteTTL),
		tokens:        apitoken.NewTokenIssuer(repos.tokens, redisStores.tokens, a.cfg.APIToken.Prefix, a.cfg.APIToken.CacheTTL),
	}
}

// jiraServices are the integration services the transport and automation layers consume.
type jiraServices struct {
	connection *integration.ConnectionService
	reports    *ticketreport.Service
}

// buildJiraIntegration wires the Jira OAuth provider, REST client, and reactive
// token manager, then the connection + ticket-report services built on them.
func (a *App) buildJiraIntegration(repos repos, redisStores redisAdapters) jiraServices {
	provider := oauth.NewJiraOAuthProvider(oauth.JiraConfig{
		ClientID: a.cfg.Jira.ClientID, ClientSecret: a.cfg.Jira.ClientSecret, RedirectURI: a.cfg.Jira.RedirectURI,
		Scopes: a.cfg.Jira.Scopes, AuthURL: a.cfg.Jira.AuthURL, TokenURL: a.cfg.Jira.TokenURL, APIBaseURL: a.cfg.Jira.APIBaseURL,
		UsePKCE: a.cfg.Jira.UsePKCE, InactivityWindow: a.cfg.Jira.InactivityWindow, HTTPTimeout: a.cfg.Jira.HTTPTimeout,
	})
	jiraClient := client.NewJiraClient(a.cfg.Jira.APIBaseURL, a.cfg.Jira.HTTPTimeout)
	tokenManager := oauthtoken.NewReactiveTokenManager(oauthtoken.Deps{
		Repo: repos.creds, Provider: provider, ProviderName: provider.Name(), Skew: a.cfg.Jira.AccessTokenSkew,
	})
	return jiraServices{
		connection: integration.NewConnectionService(integration.Deps{
			Provider: provider, State: redisStores.state, Repo: repos.creds, UsePKCE: a.cfg.Jira.UsePKCE,
		}),
		reports: ticketreport.NewService(ticketreport.Deps{
			Provider: provider.Name(), Tokens: tokenManager, Creds: repos.creds,
			Client: jiraClient, Cache: redisStores.tickets, Gate: redisStores.reconcile,
		}),
	}
}

// buildAutomation wires the blog-digest subsystem: its store, Redis seen-set, and
// the run pipeline (discover → scrape → summarize), reusing ticket reporting to file.
func (a *App) buildAutomation(pool *pgxpool.Pool, redisClient *redis.Client, reports *ticketreport.Service) {
	a.autoRepo = store.NewPostgresAutomationRepository(pool)
	a.autoSvc = automation.NewService(automation.Deps{
		Repo:            a.autoRepo,
		Discoverer:      discover.New(a.cfg.Automation.HTTPTimeout),
		Scraper:         scrape.New(a.cfg.Automation.HTTPTimeout),
		Summarizer:      summarize.New(a.cfg.Ollama.BaseURL, a.cfg.Ollama.Model, a.cfg.Ollama.Timeout, a.cfg.Ollama.MaxInputChars),
		Tickets:         reports,
		Seen:            redisstore.NewRedisAutomationSeenSet(redisClient),
		MaxPostsPerRun:  a.cfg.Automation.MaxPostsPerRun,
		DefaultInterval: a.cfg.Automation.DefaultInterval,
	})
}

// buildRouterDeps assembles the HTTP layer: identity resolution (bearer first,
// then session cookie) plus the per-feature handlers.
func (a *App) buildRouterDeps(pool *pgxpool.Pool, redisClient *redis.Client, redisStores redisAdapters, authn authServices, jira jiraServices) transport.RouterDeps {
	resolver := transport.NewChainIdentityResolver(
		transport.NewBearerTokenResolver(authn.tokens),
		transport.NewSessionIdentityResolver(authn.sessions, a.cfg.Session.CookieName),
	)
	cookie := transport.CookieConfig{
		SessionName: a.cfg.Session.CookieName, Secure: a.cfg.Session.CookieSecure,
		Domain: a.cfg.Session.CookieDomain, MaxAge: a.cfg.Session.TTL,
	}
	return transport.RouterDeps{
		Logger:      a.log,
		Resolver:    resolver,
		TLSEnabled:  a.cfg.Session.CookieSecure,
		AllowOrigin: a.cfg.HTTP.AllowedOrigins,
		Auth:        transport.NewAuthHandler(authn.authenticator, authn.sessions, redisStores.rate, cookie),
		Tokens:      transport.NewTokenHandler(authn.tokens),
		Health:      transport.NewHealthHandler(pool, platform.RedisPinger{Client: redisClient}),
		Integration: transport.NewIntegrationHandler(jira.connection, jira.reports, "/"),
		Automation:  transport.NewAutomationHandler(a.autoSvc),
	}
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
}

func newRepos(pool *pgxpool.Pool, cipher store.TokenCipher) repos {
	return repos{
		tenants: store.NewPostgresTenantRepository(pool),
		users:   store.NewPostgresUserRepository(pool),
		tokens:  store.NewPostgresApiTokenRepository(pool),
		creds:   store.NewPostgresCredentialRepository(pool, cipher),
	}
}

// redisAdapters groups the Redis-backed adapters.
type redisAdapters struct {
	sessions  *redisstore.RedisSessionStore
	tokens    *redisstore.RedisTokenCache
	rate      *redisstore.RedisRateLimiter
	state     *redisstore.RedisOAuthStateStore
	tickets   *redisstore.RedisTicketCache
	reconcile *redisstore.RedisReconcileGate
}

func newRedisAdapters(redisClient *redis.Client, cfg config.Config) redisAdapters {
	return redisAdapters{
		sessions:  redisstore.NewRedisSessionStore(redisClient),
		tokens:    redisstore.NewRedisTokenCache(redisClient),
		rate:      redisstore.NewRedisRateLimiter(redisClient, cfg.RateLimit.LoginMax, cfg.RateLimit.LoginWindow),
		state:     redisstore.NewRedisOAuthStateStore(redisClient, oauthStateTTL),
		tickets:   redisstore.NewRedisTicketCache(redisClient, ticketCacheTTL),
		reconcile: redisstore.NewRedisReconcileGate(redisClient, reconcileWindow),
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
