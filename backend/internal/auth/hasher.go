// Package auth provides password hashing and user authentication.
package auth

import (
	"context"

	"github.com/alexedwards/argon2id"
)

// PasswordHasher hashes and verifies passwords. Consumer-defined here; the
// Argon2id implementation is Argon2PasswordHasher.
type PasswordHasher interface {
	HashPassword(ctx context.Context, plain string) (string, error)
	MatchesPassword(ctx context.Context, plain, encoded string) (bool, error)
}

// Argon2Params configures the Argon2id KDF.
type Argon2Params struct {
	Memory      uint32 // KiB
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// Argon2PasswordHasher hashes passwords with Argon2id via the maintained
// github.com/alexedwards/argon2id library, which handles the random salt, the
// standard PHC encoding (parameters travel with the hash), and the constant-time
// comparison — so we don't hand-roll any of it.
type Argon2PasswordHasher struct {
	params *argon2id.Params
}

// NewArgon2PasswordHasher constructs the hasher.
func NewArgon2PasswordHasher(p Argon2Params) *Argon2PasswordHasher {
	return &Argon2PasswordHasher{params: &argon2id.Params{
		Memory:      p.Memory,
		Iterations:  p.Iterations,
		Parallelism: p.Parallelism,
		SaltLength:  p.SaltLength,
		KeyLength:   p.KeyLength,
	}}
}

// HashPassword returns a PHC-encoded Argon2id hash with a fresh random salt.
func (h *Argon2PasswordHasher) HashPassword(_ context.Context, plain string) (string, error) {
	return argon2id.CreateHash(plain, h.params)
}

// MatchesPassword reports whether plain matches the PHC-encoded hash. The library
// recomputes with the parameters embedded in the hash, so older hashes keep
// verifying, and compares in constant time.
func (h *Argon2PasswordHasher) MatchesPassword(_ context.Context, plain, encoded string) (bool, error) {
	return argon2id.ComparePasswordAndHash(plain, encoded)
}
