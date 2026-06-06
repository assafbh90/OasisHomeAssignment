// Package apitoken issues and validates machine API keys (PATs) for the REST
// API. These are OUR tokens — distinct from the third-party OAuth provider
// tokens managed in internal/integration/oauthtoken. Only a SHA-256 hash of a
// key is stored; the plaintext is shown once at creation and never again.
package apitoken

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/logging"
	"github.com/assafbh/identityhub/internal/secret"
)

// TokenRepository is the tenant-scoped token store. Consumer-defined here;
// implemented by storage/postgres.PostgresApiTokenRepository.
type TokenRepository interface {
	SaveToken(ctx context.Context, meta *domain.TokenMeta, hash []byte) error
	FindByHash(ctx context.Context, hash []byte) (domain.TokenMeta, error)
	ListByOwner(ctx context.Context, tenantID, ownerID uuid.UUID) ([]domain.TokenMeta, error)
	RevokeToken(ctx context.Context, tenantID, ownerID, tokenID uuid.UUID) ([]byte, error)
	TouchLastUsed(ctx context.Context, tenantID, tokenID uuid.UUID) error
}

// TokenCache caches validated tokens to avoid a DB hit per request. Keyed by the
// hex SHA-256 of the token. Implemented by storage/redis.RedisTokenCache.
type TokenCache interface {
	Get(ctx context.Context, key string) (domain.Identity, bool, error)
	Set(ctx context.Context, key string, id domain.Identity, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

// TokenService is the public surface consumed by handlers.
type TokenService interface {
	IssueToken(ctx context.Context, owner domain.Identity, name string, scopes []string, expiresAt *time.Time) (plaintext string, meta domain.TokenMeta, err error)
	AuthenticateToken(ctx context.Context, bearer string) (domain.Identity, error)
	ListTokens(ctx context.Context, owner domain.Identity) ([]domain.TokenMeta, error)
	RevokeToken(ctx context.Context, owner domain.Identity, tokenID uuid.UUID) error
}

// TokenIssuer implements TokenService.
type TokenIssuer struct {
	repo     TokenRepository
	cache    TokenCache
	prefix   string
	cacheTTL time.Duration
}

// NewTokenIssuer constructs the issuer. prefix is the recognizable token prefix
// (e.g. "ih_pat") used for identifiability and secret scanning.
func NewTokenIssuer(repo TokenRepository, cache TokenCache, prefix string, cacheTTL time.Duration) *TokenIssuer {
	return &TokenIssuer{repo: repo, cache: cache, prefix: prefix, cacheTTL: cacheTTL}
}

// IssueToken creates a token, persisting only its hash, and returns the
// plaintext exactly once.
func (s *TokenIssuer) IssueToken(ctx context.Context, owner domain.Identity, name string, scopes []string, expiresAt *time.Time) (string, domain.TokenMeta, error) {
	randomPart, err := secret.NewToken(secret.TokenBytes)
	if err != nil {
		return "", domain.TokenMeta{}, fmt.Errorf("generate token: %w", err)
	}
	plaintext := s.prefix + "_" + randomPart
	hash := hashToken(plaintext)

	meta := &domain.TokenMeta{
		TenantID:  owner.TenantID,
		OwnerID:   owner.UserID,
		Name:      name,
		Prefix:    s.prefix,
		Scopes:    scopes,
		ExpiresAt: expiresAt,
	}
	if err := s.repo.SaveToken(ctx, meta, hash); err != nil {
		return "", domain.TokenMeta{}, fmt.Errorf("save token: %w", err)
	}
	return plaintext, *meta, nil
}

// AuthenticateToken validates a bearer token and returns its Identity. Validated
// tokens are cached briefly; revoked/expired tokens are rejected with distinct
// sentinel errors. Returns ErrTokenNotFound for anything unrecognizable.
func (s *TokenIssuer) AuthenticateToken(ctx context.Context, bearer string) (domain.Identity, error) {
	bearer = strings.TrimSpace(bearer)
	if !strings.HasPrefix(bearer, s.prefix+"_") {
		return domain.Identity{}, domain.ErrTokenNotFound
	}
	hash := hashToken(bearer)
	key := hex.EncodeToString(hash)

	if id, ok, err := s.cache.Get(ctx, key); err == nil && ok {
		return id, nil
	}

	meta, err := s.repo.FindByHash(ctx, hash)
	if err != nil {
		return domain.Identity{}, err // ErrTokenNotFound or wrapped
	}
	if meta.IsRevoked() {
		return domain.Identity{}, domain.ErrTokenRevoked
	}
	if meta.IsExpired(time.Now()) {
		return domain.Identity{}, domain.ErrTokenExpired
	}

	id := domain.Identity{
		UserID:     meta.OwnerID,
		TenantID:   meta.TenantID,
		Scopes:     meta.Scopes,
		AuthMethod: domain.AuthMethodToken,
	}
	if err := s.cache.Set(ctx, key, id, s.cacheTTL); err != nil {
		logging.FromContext(ctx).Warn("token cache set failed", logging.Err(err))
	}
	// Best-effort last-used bookkeeping; never fail auth on this.
	if err := s.repo.TouchLastUsed(ctx, meta.TenantID, meta.ID); err != nil {
		logging.FromContext(ctx).Warn("touch token last_used failed", logging.Err(err))
	}
	return id, nil
}

// ListTokens lists the caller's tokens (metadata only).
func (s *TokenIssuer) ListTokens(ctx context.Context, owner domain.Identity) ([]domain.TokenMeta, error) {
	return s.repo.ListByOwner(ctx, owner.TenantID, owner.UserID)
}

// RevokeToken revokes one of the caller's tokens and evicts it from the cache.
func (s *TokenIssuer) RevokeToken(ctx context.Context, owner domain.Identity, tokenID uuid.UUID) error {
	hash, err := s.repo.RevokeToken(ctx, owner.TenantID, owner.UserID, tokenID)
	if err != nil {
		return err
	}
	if len(hash) > 0 {
		if err := s.cache.Delete(ctx, hex.EncodeToString(hash)); err != nil {
			logging.FromContext(ctx).Warn("token cache delete failed", logging.Err(err))
		}
	}
	return nil
}

func hashToken(plaintext string) []byte {
	sum := sha256.Sum256([]byte(plaintext))
	return sum[:]
}

// ensure interface compliance
var _ TokenService = (*TokenIssuer)(nil)
