package auth_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/auth"
	"github.com/assafbh/identityhub/internal/domain"
)

// fakeUserRepo is a small hand-written fake keyed by email (login is by email).
type fakeUserRepo struct {
	users map[string]domain.User
}

func (f *fakeUserRepo) FindUserForLogin(_ context.Context, email string) (domain.User, error) {
	u, ok := f.users[email]
	if !ok {
		return domain.User{}, domain.ErrUserNotFound
	}
	return u, nil
}

func TestUserAuthenticator_AuthenticateUser(t *testing.T) {
	t.Parallel()

	hasher := testHasher()
	tenant := uuid.New()
	userID := uuid.New()
	hash, err := hasher.HashPassword(context.Background(), "s3cret")
	require.NoError(t, err)

	repo := &fakeUserRepo{users: map[string]domain.User{
		"alice@example.com": {
			ID: userID, TenantID: tenant, Email: "alice@example.com",
			PasswordHash: hash, Status: domain.UserStatusActive,
		},
		"disabled@example.com": {
			ID: uuid.New(), TenantID: tenant, Email: "disabled@example.com",
			PasswordHash: hash, Status: domain.UserStatusDisabled,
		},
	}}
	subject := auth.NewUserAuthenticator(repo, hasher)

	tests := []struct {
		name    string
		creds   domain.Credentials
		want    domain.Identity
		wantErr error
	}{
		{
			name:  "valid credentials",
			creds: domain.Credentials{Email: "alice@example.com", Password: "s3cret"},
			want:  domain.Identity{UserID: userID, TenantID: tenant, AuthMethod: domain.AuthMethodSession},
		},
		{
			name:    "wrong password",
			creds:   domain.Credentials{Email: "alice@example.com", Password: "nope"},
			wantErr: domain.ErrInvalidCredentials,
		},
		{
			name:    "unknown user",
			creds:   domain.Credentials{Email: "ghost@example.com", Password: "s3cret"},
			wantErr: domain.ErrInvalidCredentials,
		},
		{
			name:    "disabled user",
			creds:   domain.Credentials{Email: "disabled@example.com", Password: "s3cret"},
			wantErr: domain.ErrInvalidCredentials,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := subject.AuthenticateUser(context.Background(), tc.creds)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
