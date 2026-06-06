package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/assafbh/identityhub/internal/domain"
)

// UserRepository is the user lookup the authenticator needs. Login resolves the
// user (and thus the tenant) by email alone. Consumer-defined here; implemented
// by storage/postgres.PostgresUserRepository.
type UserRepository interface {
	FindUserForLogin(ctx context.Context, email string) (domain.User, error)
}

// Authenticator verifies credentials and returns an Identity.
type Authenticator interface {
	AuthenticateUser(ctx context.Context, creds domain.Credentials) (domain.Identity, error)
}

// UserAuthenticator authenticates users against the user repository using the
// password hasher.
type UserAuthenticator struct {
	users     UserRepository
	hasher    PasswordHasher
	dummyHash string
}

// NewUserAuthenticator constructs the authenticator. It precomputes a dummy hash
// (matching the hasher's real cost) used to keep the unknown-user path
// timing-equivalent to the wrong-password path.
func NewUserAuthenticator(users UserRepository, hasher PasswordHasher) *UserAuthenticator {
	dummy, _ := hasher.HashPassword(context.Background(), uuid.NewString())
	return &UserAuthenticator{users: users, hasher: hasher, dummyHash: dummy}
}

// AuthenticateUser verifies the credentials. Unknown user and wrong password are
// indistinguishable to the caller (both return ErrInvalidCredentials), and the
// password comparison runs even for unknown users to avoid a timing oracle.
func (a *UserAuthenticator) AuthenticateUser(ctx context.Context, creds domain.Credentials) (domain.Identity, error) {
	user, err := a.users.FindUserForLogin(ctx, creds.Email)
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			// Compare against a dummy hash so timing doesn't reveal user existence.
			_, _ = a.hasher.MatchesPassword(ctx, creds.Password, a.dummyHash)
			return domain.Identity{}, domain.ErrInvalidCredentials
		}
		return domain.Identity{}, fmt.Errorf("find user: %w", err)
	}

	ok, err := a.hasher.MatchesPassword(ctx, creds.Password, user.PasswordHash)
	if err != nil {
		return domain.Identity{}, fmt.Errorf("verify password: %w", err)
	}
	if !ok || !user.IsActive() {
		return domain.Identity{}, domain.ErrInvalidCredentials
	}

	return domain.Identity{
		UserID:     user.ID,
		TenantID:   user.TenantID,
		AuthMethod: domain.AuthMethodSession,
	}, nil
}
