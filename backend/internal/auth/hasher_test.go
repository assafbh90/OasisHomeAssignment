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

// TestArgon2PasswordHasher_HashAndMatch is a smoke test of OUR adapter: that our
// Argon2Params produce an argon2id PHC hash and that MatchesPassword returns the
// right verdict for both a correct and an incorrect candidate. (We don't re-test
// argon2id's own salt/encoding behavior — that's the library's job.)
func TestArgon2PasswordHasher_HashAndMatch(t *testing.T) {
	t.Parallel()
	hasher := testHasher()
	ctx := context.Background()

	const password = "correct horse battery staple"
	encoded, err := hasher.HashPassword(ctx, password)
	require.NoError(t, err)
	require.Contains(t, encoded, "$argon2id$", "adapter should produce an argon2id PHC hash from our params")

	cases := []struct {
		name      string
		candidate string
		wantMatch bool
	}{
		{"correct password matches", password, true},
		{"wrong password does not match", "wrong", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matched, err := hasher.MatchesPassword(ctx, tc.candidate, encoded)
			require.NoError(t, err)
			require.Equal(t, tc.wantMatch, matched)
		})
	}
}
