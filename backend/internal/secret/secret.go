// Package secret generates cryptographically-random, URL-safe opaque tokens.
// It centralizes the "N random bytes -> base64url" pattern used for session IDs,
// CSRF tokens, API-key secrets, OAuth state, PKCE verifiers, and pending-action
// IDs, so the entropy size and encoding are defined once.
package secret

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

// Standard entropy sizes in bytes.
const (
	TokenBytes = 32 // 256 bits — for session IDs, CSRF, API keys, OAuth state, PKCE
	IDBytes    = 16 // 128 bits — for opaque, non-credential identifiers
)

// NewToken returns a base64url (unpadded) string of n cryptographically-random
// bytes.
func NewToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
