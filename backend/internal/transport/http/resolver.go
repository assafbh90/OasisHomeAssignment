package http

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/httpconst"
)

// IdentityResolver produces an Identity from a request, or an error. It returns
// domain.ErrUnauthenticated when no credential of its kind is present (so a chain
// can fall through), and a specific error when a credential is present but
// invalid (so the chain surfaces it rather than masking it).
type IdentityResolver interface {
	ResolveIdentity(ctx context.Context, r *http.Request) (domain.Identity, error)
}

// sessionResolver is the slice of session.Manager the resolver needs.
type sessionResolver interface {
	Resolve(ctx context.Context, sid string) (domain.Identity, error)
}

// tokenAuthenticator is the slice of apitoken.TokenService the resolver needs.
type tokenAuthenticator interface {
	AuthenticateToken(ctx context.Context, bearer string) (domain.Identity, error)
}

// SessionIdentityResolver resolves a session cookie.
type SessionIdentityResolver struct {
	sessions   sessionResolver
	cookieName string
}

// NewSessionIdentityResolver constructs the resolver.
func NewSessionIdentityResolver(sessions sessionResolver, cookieName string) *SessionIdentityResolver {
	return &SessionIdentityResolver{sessions: sessions, cookieName: cookieName}
}

// ResolveIdentity reads the session cookie and resolves it.
func (r *SessionIdentityResolver) ResolveIdentity(ctx context.Context, req *http.Request) (domain.Identity, error) {
	cookie, err := req.Cookie(r.cookieName)
	if err != nil || cookie.Value == "" {
		return domain.Identity{}, domain.ErrUnauthenticated
	}
	return r.sessions.Resolve(ctx, cookie.Value)
}

// BearerTokenResolver resolves an Authorization: Bearer machine token.
type BearerTokenResolver struct {
	tokens tokenAuthenticator
}

// NewBearerTokenResolver constructs the resolver.
func NewBearerTokenResolver(tokens tokenAuthenticator) *BearerTokenResolver {
	return &BearerTokenResolver{tokens: tokens}
}

// ResolveIdentity reads the bearer token and authenticates it.
func (r *BearerTokenResolver) ResolveIdentity(ctx context.Context, req *http.Request) (domain.Identity, error) {
	authHeader := req.Header.Get(httpconst.HeaderAuthorization)
	if !strings.HasPrefix(authHeader, httpconst.BearerPrefix) {
		return domain.Identity{}, domain.ErrUnauthenticated
	}
	token := strings.TrimSpace(strings.TrimPrefix(authHeader, httpconst.BearerPrefix))
	if token == "" {
		return domain.Identity{}, domain.ErrUnauthenticated
	}
	return r.tokens.AuthenticateToken(ctx, token)
}

// ChainIdentityResolver tries each resolver in order, falling through only when a
// resolver reports no credential of its kind (ErrUnauthenticated).
type ChainIdentityResolver struct {
	resolvers []IdentityResolver
}

// NewChainIdentityResolver constructs the chain (bearer first, then session).
func NewChainIdentityResolver(resolvers ...IdentityResolver) *ChainIdentityResolver {
	return &ChainIdentityResolver{resolvers: resolvers}
}

// ResolveIdentity walks the chain.
func (r *ChainIdentityResolver) ResolveIdentity(ctx context.Context, req *http.Request) (domain.Identity, error) {
	for _, res := range r.resolvers {
		id, err := res.ResolveIdentity(ctx, req)
		if err == nil {
			return id, nil
		}
		if errors.Is(err, domain.ErrUnauthenticated) {
			continue
		}
		return domain.Identity{}, err
	}
	return domain.Identity{}, domain.ErrUnauthenticated
}

var _ IdentityResolver = (*ChainIdentityResolver)(nil)
