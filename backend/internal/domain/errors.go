// Package domain holds the core entities, value objects, and domain errors.
// It is pure Go: it imports no transport, storage, or third-party infra.
package domain

import "errors"

// Sentinel domain errors. Adapters wrap these with %w; transport maps them to
// HTTP status codes. Client-facing messages stay generic (see transport) to
// avoid leaking which factor failed.
var (
	// Auth
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrSessionNotFound    = errors.New("session not found")
	ErrUserNotFound       = errors.New("user not found")
	ErrTenantNotFound     = errors.New("tenant not found")
	ErrTokenNotFound      = errors.New("token not found")
	ErrTokenRevoked       = errors.New("token revoked")
	ErrTokenExpired       = errors.New("token expired")

	// Authorization / tenancy
	ErrTenantMismatch  = errors.New("tenant mismatch")
	ErrForbiddenScope  = errors.New("forbidden scope")
	ErrUnauthenticated = errors.New("unauthenticated")

	// Integration
	ErrReauthRequired         = errors.New("reauth required")
	ErrCredentialNotFound     = errors.New("credential not found")
	ErrAutomationNotFound     = errors.New("automation not found")
	ErrProviderNotSupported   = errors.New("provider not supported")
	ErrCapabilityNotSupported = errors.New("capability not supported")
	ErrStateNotFound          = errors.New("oauth state not found or already used")
	ErrPendingActionNotFound  = errors.New("pending action not found")

	// ErrInvalidGrant is returned by a provider when a refresh token is no longer
	// valid (e.g. rotated/expired). It drives the connection to needs_reauth.
	ErrInvalidGrant = errors.New("invalid_grant")
)
