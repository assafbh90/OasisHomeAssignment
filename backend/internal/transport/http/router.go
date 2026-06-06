// Package http contains the gin router, middleware, request/response DTOs, and
// HTTP handlers. It is the only layer that knows about gin. Handlers read the
// authenticated Identity from the request context — never from request input.
package http

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

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
	Automation  *AutomationHandler
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
	// Interactive API docs (Swagger UI) at /api_docs/index.html.
	r.GET("/api_docs/*any", ginSwagger.WrapHandler(swaggerFiles.Handler, ginSwagger.URL("/api_docs/doc.json")))
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
		if d.Automation != nil {
			registerAutomationRoutes(v1, d.Automation)
		}
	}

	return r
}

// registerAutomationRoutes mounts the automation CRUD endpoints. These are
// session-driven (UI); unsafe methods are guarded by CSRF + session method.
func registerAutomationRoutes(v1 *gin.RouterGroup, handler *AutomationHandler) {
	automationGroup := v1.Group("/automations")
	{
		automationGroup.GET("", handler.List)
		automationGroup.POST("", RequireSessionMethod(), handler.Create)
		automationGroup.GET("/:id", handler.Get)
		automationGroup.PUT("/:id", RequireSessionMethod(), handler.Update)
		automationGroup.DELETE("/:id", RequireSessionMethod(), handler.Delete)
		automationGroup.POST("/:id/run", RequireSessionMethod(), handler.RunNow)
	}
}

// registerIntegrationRoutes mounts the Jira integration endpoints. The
// {provider} path segment is validated by middleware; handlers never branch on
// provider name.
func registerIntegrationRoutes(v1 *gin.RouterGroup, handler *IntegrationHandler) {
	integrationGroup := v1.Group("/integrations")
	{
		integrationGroup.GET("", handler.ListIntegrations)

		providerGroup := integrationGroup.Group("/:provider", ValidateProvider())
		{
			providerGroup.GET("/connect", RequireSessionMethod(), handler.Connect)
			providerGroup.GET("/callback", RequireSessionMethod(), handler.Callback)
			providerGroup.GET("/status", handler.Status)
			providerGroup.GET("/projects", handler.ListProjects)
			providerGroup.GET("/tickets", handler.ListRecentTickets)
			providerGroup.POST("/tickets", RequireScope(domain.ScopeIntegrationsWrite), handler.CreateTicket)
			providerGroup.POST("/reconcile", handler.Reconcile)
			providerGroup.DELETE("", RequireScope(domain.ScopeIntegrationsWrite), handler.Disconnect)
		}
	}
}
