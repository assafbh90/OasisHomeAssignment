// Package oauthtoken manages third-party OAuth provider tokens: their at-rest
// encryption (TokenCipher) and their reactive refresh (TokenManager). These are
// distinct from our own machine API keys (internal/apitoken).
package oauthtoken

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// AESGCMTokenCipher encrypts provider tokens with AES-256-GCM. A fresh random
// nonce is generated per encryption and prepended to the ciphertext; the GCM
// auth tag detects tampering on decrypt.
type AESGCMTokenCipher struct {
	gcm cipher.AEAD
}

// aes256KeySize is the required key length for AES-256.
const aes256KeySize = 32

// NewAESGCMTokenCipher constructs the cipher from a 32-byte (AES-256) key.
func NewAESGCMTokenCipher(key []byte) (*AESGCMTokenCipher, error) {
	if len(key) != aes256KeySize {
		return nil, fmt.Errorf("token cipher key must be %d bytes, got %d", aes256KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	return &AESGCMTokenCipher{gcm: gcm}, nil
}

// EncryptToken returns nonce||ciphertext.
func (c *AESGCMTokenCipher) EncryptToken(plaintext string) ([]byte, error) {
	nonce := make([]byte, c.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	// Seal appends the ciphertext to nonce, so the result is nonce||ciphertext.
	return c.gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// ErrInvalidCiphertext indicates a malformed or tampered ciphertext.
var ErrInvalidCiphertext = errors.New("invalid token ciphertext")

// DecryptToken reverses EncryptToken, failing closed on tampering.
func (c *AESGCMTokenCipher) DecryptToken(ciphertext []byte) (string, error) {
	nonceSize := c.gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", ErrInvalidCiphertext
	}
	nonce, cipherBytes := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := c.gcm.Open(nil, nonce, cipherBytes, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidCiphertext, err)
	}
	return string(plaintext), nil
}
