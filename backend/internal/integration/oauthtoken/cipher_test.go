package oauthtoken_test

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/integration/oauthtoken"
)

func newCipher(t *testing.T) *oauthtoken.AESGCMTokenCipher {
	t.Helper()
	key := make([]byte, 32)
	_, err := rand.Read(key)
	require.NoError(t, err)
	c, err := oauthtoken.NewAESGCMTokenCipher(key)
	require.NoError(t, err)
	return c
}

func TestAESGCMTokenCipher_RoundTrip(t *testing.T) {
	t.Parallel()
	c := newCipher(t)

	ct, err := c.EncryptToken("super-secret-refresh-token")
	require.NoError(t, err)

	pt, err := c.DecryptToken(ct)
	require.NoError(t, err)
	require.Equal(t, "super-secret-refresh-token", pt)
}

func TestAESGCMTokenCipher_TamperDetected(t *testing.T) {
	t.Parallel()
	c := newCipher(t)

	ct, err := c.EncryptToken("secret")
	require.NoError(t, err)
	ct[len(ct)-1] ^= 0xFF // flip a bit in the tag/ciphertext

	_, err = c.DecryptToken(ct)
	require.ErrorIs(t, err, oauthtoken.ErrInvalidCiphertext)
}

func TestAESGCMTokenCipher_UniqueNonces(t *testing.T) {
	t.Parallel()
	c := newCipher(t)

	a, err := c.EncryptToken("same")
	require.NoError(t, err)
	b, err := c.EncryptToken("same")
	require.NoError(t, err)

	require.False(t, bytes.Equal(a, b), "same plaintext must produce different ciphertexts")
}

func TestAESGCMTokenCipher_RejectsBadKey(t *testing.T) {
	t.Parallel()
	_, err := oauthtoken.NewAESGCMTokenCipher([]byte("too-short"))
	require.Error(t, err)
}

func TestAESGCMTokenCipher_ShortCiphertext(t *testing.T) {
	t.Parallel()
	c := newCipher(t)
	_, err := c.DecryptToken([]byte{0x01, 0x02})
	require.ErrorIs(t, err, oauthtoken.ErrInvalidCiphertext)
}
