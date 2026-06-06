package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/session"
)

// fakeStore is an in-memory session.Store that ignores TTLs (Manager policy is
// what we test here).
type fakeStore struct {
	data    map[string]session.SessionData
	deleted []string
}

func newFakeStore() *fakeStore { return &fakeStore{data: map[string]session.SessionData{}} }

func (f *fakeStore) Save(_ context.Context, id string, d session.SessionData, _ time.Duration) error {
	f.data[id] = d
	return nil
}

func (f *fakeStore) Find(_ context.Context, id string) (session.SessionData, error) {
	d, ok := f.data[id]
	if !ok {
		return session.SessionData{}, domain.ErrSessionNotFound
	}
	return d, nil
}

func (f *fakeStore) Refresh(_ context.Context, id string, _ time.Duration) error {
	if _, ok := f.data[id]; !ok {
		return domain.ErrSessionNotFound
	}
	return nil
}

func (f *fakeStore) Delete(_ context.Context, id string) error {
	delete(f.data, id)
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeStore) DeleteAllForUser(_ context.Context, u uuid.UUID) error {
	for id, d := range f.data {
		if d.UserID == u {
			delete(f.data, id)
		}
	}
	return nil
}

func TestManager_CreateThenResolve(t *testing.T) {
	t.Parallel()
	// Arrange
	store := newFakeStore()
	m := session.NewManager(store, time.Hour, 24*time.Hour)
	id := domain.Identity{UserID: uuid.New(), TenantID: uuid.New(), AuthMethod: domain.AuthMethodSession}

	// Act
	sid, err := m.Create(context.Background(), id)
	require.NoError(t, err)

	got, err := m.Resolve(context.Background(), sid)

	// Assert
	require.NoError(t, err)
	require.Equal(t, id.UserID, got.UserID)
	require.Equal(t, id.TenantID, got.TenantID)
	require.Equal(t, domain.AuthMethodSession, got.AuthMethod)
	require.NotEmpty(t, sid)
}

func TestManager_Resolve_Unknown(t *testing.T) {
	t.Parallel()
	m := session.NewManager(newFakeStore(), time.Hour, 24*time.Hour)
	_, err := m.Resolve(context.Background(), "nope")
	require.ErrorIs(t, err, domain.ErrSessionNotFound)
}

func TestManager_Resolve_PastAbsoluteLifetime(t *testing.T) {
	t.Parallel()
	// Arrange: a session whose absolute lifetime is already in the past.
	store := newFakeStore()
	m := session.NewManager(store, time.Hour, 24*time.Hour)
	sid := "expired-sid"
	store.data[sid] = session.SessionData{
		UserID:            uuid.New(),
		TenantID:          uuid.New(),
		CreatedAt:         time.Now().Add(-48 * time.Hour),
		AbsoluteExpiresAt: time.Now().Add(-time.Minute),
	}

	// Act
	_, err := m.Resolve(context.Background(), sid)

	// Assert
	require.ErrorIs(t, err, domain.ErrSessionNotFound)
	require.Contains(t, store.deleted, sid, "expired session should be deleted")
}

func TestManager_Create_TTLBoundedByAbsolute(t *testing.T) {
	t.Parallel()
	// Sliding TTL (48h) exceeds the absolute lifetime (1h), so it must be capped
	// to the remaining absolute window.
	store := newFakeStore()
	m := session.NewManager(store, 48*time.Hour, time.Hour)
	id := domain.Identity{UserID: uuid.New(), TenantID: uuid.New(), AuthMethod: domain.AuthMethodSession}

	sid, err := m.Create(context.Background(), id)
	require.NoError(t, err)
	got, err := m.Resolve(context.Background(), sid)
	require.NoError(t, err)
	require.Equal(t, id.UserID, got.UserID)
}

func TestManager_RevokeAllForUser(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	m := session.NewManager(store, time.Hour, 24*time.Hour)
	user := uuid.New()
	id := domain.Identity{UserID: user, TenantID: uuid.New(), AuthMethod: domain.AuthMethodSession}
	s1, _ := m.Create(context.Background(), id)
	s2, _ := m.Create(context.Background(), id)

	require.NoError(t, m.RevokeAllForUser(context.Background(), user))

	_, err1 := m.Resolve(context.Background(), s1)
	_, err2 := m.Resolve(context.Background(), s2)
	require.ErrorIs(t, err1, domain.ErrSessionNotFound)
	require.ErrorIs(t, err2, domain.ErrSessionNotFound)
}
