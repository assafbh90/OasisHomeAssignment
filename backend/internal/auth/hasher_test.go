package auth_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/auth"
)

func testHasher() *auth.Argon2PasswordHasher {
	// Low cost for fast tests.
	return auth.NewArgon2PasswordHasher(auth.Argon2Params{
		Memory: 8 * 1024, Iterations: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32,
	})
}

func TestArgon2PasswordHasher_RoundTrip(t *testing.T) {
	t.Parallel()
	// Arrange
	h := testHasher()
	ctx := context.Background()

	// Act
	encoded, err := h.HashPassword(ctx, "correct horse battery staple")
	require.NoError(t, err)

	// Assert
	require.Contains(t, encoded, "$argon2id$")
	ok, err := h.MatchesPassword(ctx, "correct horse battery staple", encoded)
	require.NoError(t, err)
	require.True(t, ok)
}

func TestArgon2PasswordHasher_WrongPassword(t *testing.T) {
	t.Parallel()
	h := testHasher()
	ctx := context.Background()

	encoded, err := h.HashPassword(ctx, "right")
	require.NoError(t, err)

	ok, err := h.MatchesPassword(ctx, "wrong", encoded)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestArgon2PasswordHasher_DistinctSaltsProduceDistinctHashes(t *testing.T) {
	t.Parallel()
	h := testHasher()
	ctx := context.Background()

	a, err := h.HashPassword(ctx, "same")
	require.NoError(t, err)
	b, err := h.HashPassword(ctx, "same")
	require.NoError(t, err)

	require.NotEqual(t, a, b, "random salt should make hashes differ")
}
