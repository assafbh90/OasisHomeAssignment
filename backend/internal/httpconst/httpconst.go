// Package httpconst centralizes HTTP header names, schemes, media types, and the
// default outbound client timeout that are used across multiple packages
// (transport, oauth, connector), so the same literal isn't duplicated per call site.
package httpconst

import "time"

const (
	HeaderAuthorization = "Authorization"
	HeaderContentType   = "Content-Type"
	HeaderAccept        = "Accept"
	HeaderRetryAfter    = "Retry-After"

	ContentTypeJSON = "application/json"

	// SchemeBearer is the bearer auth scheme; BearerPrefix is the value prefix on
	// the Authorization header ("Bearer <token>").
	SchemeBearer = "Bearer"
	BearerPrefix = SchemeBearer + " "
)

// DefaultClientTimeout is the fallback timeout for outbound provider HTTP calls
// (oauth + connector) when none is configured.
const DefaultClientTimeout = 10 * time.Second
