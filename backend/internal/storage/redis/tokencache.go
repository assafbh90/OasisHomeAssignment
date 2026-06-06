package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/assafbh/identityhub/internal/apitoken"
	"github.com/assafbh/identityhub/internal/domain"
)

// RedisTokenCache caches validated API-key identities at `tokencache:{key}`,
// where key is the hex SHA-256 of the token.
type RedisTokenCache struct {
	client *goredis.Client
}

// NewRedisTokenCache constructs the cache.
func NewRedisTokenCache(client *goredis.Client) *RedisTokenCache {
	return &RedisTokenCache{client: client}
}

const tokenCacheKeyPrefix = "tokencache:"

func tokenCacheKey(key string) string { return tokenCacheKeyPrefix + key }

// cachedIdentity is the serialized form (Identity isn't directly JSON-friendly
// for our needs, but its fields are).
type cachedIdentity struct {
	UserID     string   `json:"user_id"`
	TenantID   string   `json:"tenant_id"`
	Scopes     []string `json:"scopes"`
	AuthMethod string   `json:"auth_method"`
}

// Get returns a cached identity if present.
func (c *RedisTokenCache) Get(ctx context.Context, key string) (domain.Identity, bool, error) {
	var ci cachedIdentity
	found, err := getJSON(ctx, c.client, tokenCacheKey(key), &ci)
	if err != nil || !found {
		return domain.Identity{}, false, err
	}
	id, err := ci.toIdentity()
	if err != nil {
		return domain.Identity{}, false, err
	}
	return id, true, nil
}

// Set caches a validated identity with a short TTL.
func (c *RedisTokenCache) Set(ctx context.Context, key string, id domain.Identity, ttl time.Duration) error {
	return setJSON(ctx, c.client, tokenCacheKey(key), cachedIdentity{
		UserID:     id.UserID.String(),
		TenantID:   id.TenantID.String(),
		Scopes:     id.Scopes,
		AuthMethod: string(id.AuthMethod),
	}, ttl)
}

// Delete evicts a cached identity (called on revoke).
func (c *RedisTokenCache) Delete(ctx context.Context, key string) error {
	if err := c.client.Del(ctx, tokenCacheKey(key)).Err(); err != nil {
		return fmt.Errorf("delete token cache: %w", err)
	}
	return nil
}

func (ci cachedIdentity) toIdentity() (domain.Identity, error) {
	uid, err := uuidParse(ci.UserID)
	if err != nil {
		return domain.Identity{}, err
	}
	tid, err := uuidParse(ci.TenantID)
	if err != nil {
		return domain.Identity{}, err
	}
	return domain.Identity{
		UserID:     uid,
		TenantID:   tid,
		Scopes:     ci.Scopes,
		AuthMethod: domain.AuthMethod(ci.AuthMethod),
	}, nil
}

var _ apitoken.TokenCache = (*RedisTokenCache)(nil)
