package http

import (
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/assafbh/identityhub/internal/domain"
)

// RouterDeps are the dependencies needed to build the HTTP router.
type RouterDeps struct {
	Logger      *slog.Logger
	Resolver    IdentityResolver
	TLSEnabled  bool
	AllowOrigin []string

	Auth        *AuthHandler
	Tokens      *TokenHandler
	Health      *HealthHandler
	Integration *IntegrationHandler
}

// NewRouter wires middleware and routes into a gin engine.
func NewRouter(d RouterDeps) *gin.Engine {
	r := gin.New()
	r.Use(Recovery(), RequestID(d.Logger), SecureHeaders(d.TLSEnabled))
	if len(d.AllowOrigin) > 0 {
		r.Use(CORS(d.AllowOrigin))
	}

	// Public.
	r.GET("/healthz", d.Health.Live)
	r.GET("/readyz", d.Health.Ready)
	r.POST("/v1/auth/login", d.Auth.Login)

	// Authenticated. CSRF guards cookie-authenticated unsafe methods.
	v1 := r.Group("/v1", RequireAuth(d.Resolver), CSRF())
	{
		v1.POST("/auth/logout", RequireSessionMethod(), d.Auth.Logout)
		v1.GET("/auth/me", d.Auth.Me)

		v1.POST("/tokens", RequireSessionMethod(), d.Tokens.Issue)
		v1.GET("/tokens", d.Tokens.List)
		v1.DELETE("/tokens/:id", d.Tokens.Revoke)

		if d.Integration != nil {
			registerIntegrationRoutes(v1, d.Integration)
		}
	}

	return r
}

// registerIntegrationRoutes mounts the Jira integration endpoints. The
// {provider} path segment is validated by middleware; handlers never branch on
// provider name.
func registerIntegrationRoutes(v1 *gin.RouterGroup, h *IntegrationHandler) {
	ig := v1.Group("/integrations")
	{
		ig.GET("", h.ListIntegrations)

		p := ig.Group("/:provider", ValidateProvider())
		{
			p.GET("/connect", RequireSessionMethod(), h.Connect)
			p.GET("/callback", RequireSessionMethod(), h.Callback)
			p.GET("/status", h.Status)
			p.GET("/projects", h.ListProjects)
			p.GET("/tickets", h.ListRecentTickets)
			p.POST("/tickets", RequireScope(domain.ScopeIntegrationsWrite), h.CreateTicket)
			p.DELETE("", RequireScope(domain.ScopeIntegrationsWrite), h.Disconnect)
		}
	}
}
