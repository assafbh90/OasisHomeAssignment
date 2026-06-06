package http

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/logging"
)

// API error codes returned in the `error` field of the JSON error envelope.
// They form the API's error contract (the SPA and API clients switch on them),
// and some are returned from more than one handler.
const (
	errCodeInvalidCredentials     = "invalid_credentials"
	errCodeUnauthenticated        = "unauthenticated"
	errCodeForbidden              = "forbidden"
	errCodeProviderNotSupported   = "provider_not_supported"
	errCodeCapabilityNotSupported = "capability_not_supported"
	errCodeNotConnected           = "not_connected"
	errCodeInvalidState           = "invalid_state"
	errCodeInternal               = "internal_error"
	errCodeInvalidRequest         = "invalid_request"
	errCodeReauthRequired         = "reauth_required"
	errCodeCSRFFailed             = "csrf_failed"
	errCodeRateLimited            = "rate_limited"
	errCodeNotFound               = "not_found"
)

// errorResponse is the uniform error envelope returned to clients.
type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message,omitempty"`
}

// respondError maps a domain/internal error to an HTTP status + safe message.
// Sensitive distinctions (which auth factor failed) are collapsed into generic
// messages; unexpected errors are logged and surfaced as 500.
func respondError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidCredentials):
		c.JSON(http.StatusUnauthorized, errorResponse{Error: errCodeInvalidCredentials, Message: "invalid email or password"})
	case errors.Is(err, domain.ErrUnauthenticated),
		errors.Is(err, domain.ErrSessionNotFound),
		errors.Is(err, domain.ErrTokenNotFound),
		errors.Is(err, domain.ErrTokenRevoked),
		errors.Is(err, domain.ErrTokenExpired):
		c.JSON(http.StatusUnauthorized, errorResponse{Error: errCodeUnauthenticated, Message: "authentication required"})
	case errors.Is(err, domain.ErrForbiddenScope), errors.Is(err, domain.ErrTenantMismatch):
		c.JSON(http.StatusForbidden, errorResponse{Error: errCodeForbidden, Message: "insufficient permissions"})
	case errors.Is(err, domain.ErrProviderNotSupported):
		c.JSON(http.StatusNotFound, errorResponse{Error: errCodeProviderNotSupported, Message: "unknown integration provider"})
	case errors.Is(err, domain.ErrCapabilityNotSupported):
		c.JSON(http.StatusUnprocessableEntity, errorResponse{Error: errCodeCapabilityNotSupported, Message: "operation not supported by provider"})
	case errors.Is(err, domain.ErrCredentialNotFound):
		c.JSON(http.StatusNotFound, errorResponse{Error: errCodeNotConnected, Message: "integration is not connected"})
	case errors.Is(err, domain.ErrStateNotFound):
		c.JSON(http.StatusBadRequest, errorResponse{Error: errCodeInvalidState, Message: "invalid or expired authorization state"})
	case errors.Is(err, domain.ErrAutomationNotFound):
		c.JSON(http.StatusNotFound, errorResponse{Error: errCodeNotFound, Message: "automation not found"})
	default:
		logging.FromContext(c.Request.Context()).Error("unhandled error", logging.Err(err))
		c.JSON(http.StatusInternalServerError, errorResponse{Error: errCodeInternal, Message: "an unexpected error occurred"})
	}
}

// respondValidation returns a 400 for input validation failures.
func respondValidation(c *gin.Context, msg string) {
	c.JSON(http.StatusBadRequest, errorResponse{Error: errCodeInvalidRequest, Message: msg})
}
