package http

import (
	"context"
	"crypto/subtle"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/logging"
)

// --- authenticated-identity request context ---------------------------------

type ctxKey int

const identityKey ctxKey = iota

// withIdentity stores the resolved identity in ctx.
func withIdentity(ctx context.Context, id domain.Identity) context.Context {
	return context.WithValue(ctx, identityKey, id)
}

// identityFromContext returns the identity stored in ctx, if any.
func identityFromContext(ctx context.Context) (domain.Identity, bool) {
	id, ok := ctx.Value(identityKey).(domain.Identity)
	return id, ok
}

// setIdentity attaches the identity to the gin request's context.
func setIdentity(c *gin.Context, id domain.Identity) {
	c.Request = c.Request.WithContext(withIdentity(c.Request.Context(), id))
}

// mustIdentity returns the identity from the gin request context. It is only
// called from handlers behind the auth middleware, which guarantees presence.
func mustIdentity(c *gin.Context) (domain.Identity, bool) {
	return identityFromContext(c.Request.Context())
}

const (
	headerRequestID = "X-Request-ID"
	headerCSRFToken = "X-CSRF-Token"
	csrfCookieName  = "ih_csrf"
)

// RequestID assigns/propagates a request ID, attaches it to the context and a
// request-scoped logger, and echoes it in the response.
func RequestID(base *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(headerRequestID)
		if id == "" {
			id = uuid.NewString()
		}
		c.Writer.Header().Set(headerRequestID, id)

		log := base.With(slog.String(logging.KeyRequestID, id),
			slog.String(logging.KeyMethod, c.Request.Method),
			slog.String(logging.KeyPath, c.FullPath()))
		ctx := logging.WithRequestID(c.Request.Context(), id)
		ctx = logging.WithLogger(ctx, log)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

// Recovery converts panics into 500s without leaking internals.
func Recovery() gin.HandlerFunc {
	return gin.CustomRecovery(func(c *gin.Context, recovered any) {
		logging.FromContext(c.Request.Context()).Error("panic recovered", slog.Any("panic", recovered))
		c.AbortWithStatusJSON(http.StatusInternalServerError,
			errorResponse{Error: errCodeInternal, Message: "an unexpected error occurred"})
	})
}

// SecureHeaders sets conservative security headers. HSTS is only sent when the
// deployment is behind TLS (cookieSecure mirrors that).
func SecureHeaders(tlsEnabled bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		if tlsEnabled {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	}
}

// RequireAuth resolves the caller's Identity and aborts with 401 if absent or
// invalid. On success the identity is attached to the request context and the
// logger is enriched with tenant/user.
func RequireAuth(resolver IdentityResolver) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := resolver.ResolveIdentity(c.Request.Context(), c.Request)
		if err != nil {
			respondError(c, err)
			c.Abort()
			return
		}
		setIdentity(c, id)
		log := logging.FromContext(c.Request.Context()).With(
			slog.String(logging.KeyTenantID, id.TenantID.String()),
			slog.String(logging.KeyUserID, id.UserID.String()),
			slog.String(logging.KeyAuthMethod, string(id.AuthMethod)))
		c.Request = c.Request.WithContext(logging.WithLogger(c.Request.Context(), log))
		c.Next()
	}
}

// RequireScope enforces that the caller holds scope (sessions are full-scope).
func RequireScope(scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := mustIdentity(c)
		if !ok || !id.HasScope(scope) {
			respondError(c, domain.ErrForbiddenScope)
			c.Abort()
			return
		}
		c.Next()
	}
}

// RequireSessionMethod restricts a route to interactive sessions (e.g. the OAuth
// connect/callback flow, which a machine token must not drive).
func RequireSessionMethod() gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := mustIdentity(c)
		if !ok || id.AuthMethod != domain.AuthMethodSession {
			respondError(c, domain.ErrForbiddenScope)
			c.Abort()
			return
		}
		c.Next()
	}
}

// CSRF enforces a double-submit token on unsafe methods for cookie-authenticated
// requests. Bearer-token requests carry no ambient cookie and are exempt.
func CSRF() gin.HandlerFunc {
	return func(c *gin.Context) {
		if isSafeMethod(c.Request.Method) {
			c.Next()
			return
		}
		cookie, err := c.Request.Cookie(csrfCookieName)
		if err != nil || cookie.Value == "" {
			// No CSRF cookie => not a cookie-authenticated browser flow (e.g. a
			// machine token request). Let the auth layer decide.
			c.Next()
			return
		}
		header := c.GetHeader(headerCSRFToken)
		if subtle.ConstantTimeCompare([]byte(header), []byte(cookie.Value)) != 1 {
			c.AbortWithStatusJSON(http.StatusForbidden,
				errorResponse{Error: errCodeCSRFFailed, Message: "missing or invalid CSRF token"})
			return
		}
		c.Next()
	}
}

// CORS enables credentialed cross-origin requests from the configured origins.
// The default deployment serves the SPA same-origin (via the frontend proxy),
// so this is only needed when the SPA runs on a different origin in dev.
func CORS(allowed []string) gin.HandlerFunc {
	allow := make(map[string]bool, len(allowed))
	for _, o := range allowed {
		allow[o] = true
	}
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && allow[origin] {
			h := c.Writer.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Vary", "Origin")
			h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, "+headerCSRFToken)
			h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		}
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

func isSafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}
