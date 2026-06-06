// Package redis holds the Redis-backed adapters: sessions, token cache, rate
// limiter, OAuth state, and pending actions. Each adapter owns a key namespace.
package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	goredis "github.com/redis/go-redis/v9"
	"github.com/samber/lo"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/session"
)

// RedisSessionStore stores sessions at `session:{id}` and maintains a per-user
// index set `usersessions:{userID}` so all of a user's sessions can be revoked.
type RedisSessionStore struct {
	client *goredis.Client
}

// NewRedisSessionStore constructs the store.
func NewRedisSessionStore(client *goredis.Client) *RedisSessionStore {
	return &RedisSessionStore{client: client}
}

const (
	sessionKeyPrefix      = "session:"
	userSessionsKeyPrefix = "usersessions:"
)

func sessionKey(id string) string        { return sessionKeyPrefix + id }
func userSessionsKey(u uuid.UUID) string { return userSessionsKeyPrefix + u.String() }

// Save persists session data with a TTL and indexes it under the user.
func (s *RedisSessionStore) Save(ctx context.Context, id string, data session.SessionData, ttl time.Duration) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}
	pipe := s.client.TxPipeline()
	pipe.Set(ctx, sessionKey(id), raw, ttl)
	pipe.SAdd(ctx, userSessionsKey(data.UserID), id)
	// Keep the index alive at least as long as the longest possible session.
	pipe.Expire(ctx, userSessionsKey(data.UserID), data.AbsoluteExpiresAt.Sub(data.CreatedAt)+time.Hour)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("save session: %w", err)
	}
	return nil
}

// Find loads session data, returning domain.ErrSessionNotFound if absent.
func (s *RedisSessionStore) Find(ctx context.Context, id string) (session.SessionData, error) {
	var data session.SessionData
	found, err := getJSON(ctx, s.client, sessionKey(id), &data)
	if err != nil {
		return session.SessionData{}, err
	}
	if !found {
		return session.SessionData{}, domain.ErrSessionNotFound
	}
	return data, nil
}

// Refresh extends a session's TTL (sliding expiration).
func (s *RedisSessionStore) Refresh(ctx context.Context, id string, ttl time.Duration) error {
	ok, err := s.client.Expire(ctx, sessionKey(id), ttl).Result()
	if err != nil {
		return fmt.Errorf("refresh session: %w", err)
	}
	if !ok {
		return domain.ErrSessionNotFound
	}
	return nil
}

// Delete removes a single session. The user-index entry is left to expire; it is
// also cleaned opportunistically when the data is gone.
func (s *RedisSessionStore) Delete(ctx context.Context, id string) error {
	if err := s.client.Del(ctx, sessionKey(id)).Err(); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteAllForUser removes every session indexed for a user.
func (s *RedisSessionStore) DeleteAllForUser(ctx context.Context, userID uuid.UUID) error {
	ids, err := s.client.SMembers(ctx, userSessionsKey(userID)).Result()
	if err != nil {
		return fmt.Errorf("list user sessions: %w", err)
	}
	keys := lo.Map(ids, func(id string, _ int) string { return sessionKey(id) })
	keys = append(keys, userSessionsKey(userID))
	if err := s.client.Del(ctx, keys...).Err(); err != nil {
		return fmt.Errorf("delete user sessions: %w", err)
	}
	return nil
}

var _ session.Store = (*RedisSessionStore)(nil)

// --- shared helpers, used by every store in this package ---------------------

// setJSON marshals v and stores it at key with the given TTL.
func setJSON(ctx context.Context, client *goredis.Client, key string, v any, ttl time.Duration) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal %s: %w", key, err)
	}
	if err := client.Set(ctx, key, raw, ttl).Err(); err != nil {
		return fmt.Errorf("set %s: %w", key, err)
	}
	return nil
}

// getJSON fetches key and unmarshals into out. found is false (with a nil error)
// when the key is absent, so callers decide whether a miss is an error.
func getJSON(ctx context.Context, client *goredis.Client, key string, out any) (bool, error) {
	raw, err := client.Get(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return false, nil
		}
		return false, fmt.Errorf("get %s: %w", key, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return false, fmt.Errorf("unmarshal %s: %w", key, err)
	}
	return true, nil
}

// getDelJSON atomically fetches+deletes key (a one-time read) into out, returning
// notFound if the key is absent or already consumed.
func getDelJSON(ctx context.Context, client *goredis.Client, key string, out any, notFound error) error {
	raw, err := client.GetDel(ctx, key).Bytes()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return notFound
		}
		return fmt.Errorf("getdel %s: %w", key, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("unmarshal %s: %w", key, err)
	}
	return nil
}

// uuidParse parses a UUID string, wrapping the error with the offending value.
func uuidParse(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, fmt.Errorf("parse uuid %q: %w", s, err)
	}
	return id, nil
}
