// Package session manages opaque, server-side browser sessions. Session IDs are
// high-entropy random tokens; all state lives in the Store (Redis), making
// sessions instantly revocable and leaking nothing into the cookie.
package session

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/secret"
)

// SessionData is the server-side state behind a session ID.
type SessionData struct {
	UserID            uuid.UUID `json:"user_id"`
	TenantID          uuid.UUID `json:"tenant_id"`
	CreatedAt         time.Time `json:"created_at"`
	AbsoluteExpiresAt time.Time `json:"absolute_expires_at"`
}

// Store is the low-level session key-value store. Consumer-defined here;
// implemented by storage/redis.RedisSessionStore.
type Store interface {
	Save(ctx context.Context, id string, data SessionData, ttl time.Duration) error
	Find(ctx context.Context, id string) (SessionData, error) // domain.ErrSessionNotFound
	Refresh(ctx context.Context, id string, ttl time.Duration) error
	Delete(ctx context.Context, id string) error
	DeleteAllForUser(ctx context.Context, userID uuid.UUID) error
}

// Manager applies session policy (sliding TTL bounded by an absolute lifetime)
// on top of the Store.
type Manager struct {
	store    Store
	ttl      time.Duration // sliding TTL refreshed on activity
	absolute time.Duration // hard maximum lifetime
}

// NewManager constructs the session manager.
func NewManager(store Store, ttl, absolute time.Duration) *Manager {
	return &Manager{store: store, ttl: ttl, absolute: absolute}
}

// Create starts a new session for the identity and returns the opaque session ID.
func (m *Manager) Create(ctx context.Context, id domain.Identity) (string, error) {
	sid, err := generateSessionID()
	if err != nil {
		return "", err
	}
	now := time.Now()
	data := SessionData{
		UserID:            id.UserID,
		TenantID:          id.TenantID,
		CreatedAt:         now,
		AbsoluteExpiresAt: now.Add(m.absolute),
	}
	if err := m.store.Save(ctx, sid, data, m.slidingTTL(now, data.AbsoluteExpiresAt)); err != nil {
		return "", fmt.Errorf("save session: %w", err)
	}
	return sid, nil
}

// Resolve validates a session ID and returns the Identity, refreshing the
// sliding TTL. Sessions past their absolute lifetime are deleted and rejected.
func (m *Manager) Resolve(ctx context.Context, sid string) (domain.Identity, error) {
	data, err := m.store.Find(ctx, sid)
	if err != nil {
		return domain.Identity{}, err
	}
	now := time.Now()
	if !now.Before(data.AbsoluteExpiresAt) {
		_ = m.store.Delete(ctx, sid)
		return domain.Identity{}, domain.ErrSessionNotFound
	}
	if err := m.store.Refresh(ctx, sid, m.slidingTTL(now, data.AbsoluteExpiresAt)); err != nil {
		return domain.Identity{}, fmt.Errorf("refresh session: %w", err)
	}
	return domain.Identity{
		UserID:     data.UserID,
		TenantID:   data.TenantID,
		AuthMethod: domain.AuthMethodSession,
	}, nil
}

// Revoke deletes a single session.
func (m *Manager) Revoke(ctx context.Context, sid string) error {
	return m.store.Delete(ctx, sid)
}

// RevokeAllForUser deletes all of a user's sessions (e.g. on password change).
func (m *Manager) RevokeAllForUser(ctx context.Context, userID uuid.UUID) error {
	return m.store.DeleteAllForUser(ctx, userID)
}

// slidingTTL is the sliding window, capped so it never extends past the absolute
// expiry.
func (m *Manager) slidingTTL(now, absolute time.Time) time.Duration {
	remaining := time.Until(absolute)
	if now.Add(m.ttl).Before(absolute) {
		return m.ttl
	}
	return remaining
}

func generateSessionID() (string, error) {
	id, err := secret.NewToken(secret.TokenBytes)
	if err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return id, nil
}
