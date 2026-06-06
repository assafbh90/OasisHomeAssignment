// Package http contains the gin router, middleware, request/response DTOs, and
// HTTP handlers. It is the only layer that knows about gin. Handlers read the
// authenticated Identity from the request context — never from request input.
package http

import (
	"context"

	"github.com/gin-gonic/gin"

	"github.com/assafbh/identityhub/internal/domain"
)

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
