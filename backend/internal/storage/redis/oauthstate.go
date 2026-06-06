package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/secret"
)

// RedisOAuthStateStore stores one-time OAuth `state` values bound to the
// initiating {tenant,user} plus the PKCE code_verifier, at `oauthstate:{state}`.
// Consumption is atomic (GETDEL) so a state can be used exactly once — defending
// CSRF and replay on the callback.
type RedisOAuthStateStore struct {
	client *goredis.Client
	ttl    time.Duration
}

// NewRedisOAuthStateStore constructs the store.
func NewRedisOAuthStateStore(client *goredis.Client, ttl time.Duration) *RedisOAuthStateStore {
	return &RedisOAuthStateStore{client: client, ttl: ttl}
}

type stateData struct {
	TenantID     uuid.UUID `json:"tenant_id"`
	UserID       uuid.UUID `json:"user_id"`
	CodeVerifier string    `json:"code_verifier"`
}

const stateKeyPrefix = "oauthstate:"

func stateKey(state string) string { return stateKeyPrefix + state }

// GenerateState creates and stores a fresh state bound to the identity and PKCE
// verifier, returning the opaque state string.
func (s *RedisOAuthStateStore) GenerateState(ctx context.Context, tenantID, userID uuid.UUID, codeVerifier string) (string, error) {
	state, err := secret.NewToken(secret.TokenBytes)
	if err != nil {
		return "", fmt.Errorf("generate state: %w", err)
	}
	data := stateData{TenantID: tenantID, UserID: userID, CodeVerifier: codeVerifier}
	if err := setJSON(ctx, s.client, stateKey(state), data, s.ttl); err != nil {
		return "", err
	}
	return state, nil
}

// ConsumeState atomically fetches and deletes a state, returning its binding.
// Returns domain.ErrStateNotFound if missing or already used.
func (s *RedisOAuthStateStore) ConsumeState(ctx context.Context, state string) (uuid.UUID, uuid.UUID, string, error) {
	var d stateData
	if err := getDelJSON(ctx, s.client, stateKey(state), &d, domain.ErrStateNotFound); err != nil {
		return uuid.Nil, uuid.Nil, "", err
	}
	return d.TenantID, d.UserID, d.CodeVerifier, nil
}
