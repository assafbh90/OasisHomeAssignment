package http

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/httpconst"
	"github.com/assafbh/identityhub/internal/logging"
)

// --- consumer interfaces (defined where they're used) ---

type authenticatorService interface {
	AuthenticateUser(ctx context.Context, creds domain.Credentials) (domain.Identity, error)
}

type sessionCreator interface {
	Create(ctx context.Context, id domain.Identity) (string, error)
	Revoke(ctx context.Context, sid string) error
}

type rateLimiter interface {
	AllowAttempt(ctx context.Context, key string) (bool, time.Duration, error)
}

// AuthHandler serves login/logout/me.
type AuthHandler struct {
	authn   authenticatorService
	session sessionCreator
	limiter rateLimiter
	cookie  CookieConfig
}

// NewAuthHandler constructs the handler.
func NewAuthHandler(authn authenticatorService, session sessionCreator, limiter rateLimiter, cookie CookieConfig) *AuthHandler {
	return &AuthHandler{authn: authn, session: session, limiter: limiter, cookie: cookie}
}

type loginResponse struct {
	identityResponse
	CSRFToken string `json:"csrf_token"`
}

// Login verifies credentials, creates a session, and sets cookies. It is a
// browser/session flow used by the SPA, so it's intentionally not in the public
// API docs (which cover the machine API consumed with a Bearer key).
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := bindJSON(c, &req); err != nil {
		respondValidation(c, "invalid request body")
		return
	}
	if msg := req.validate(); msg != "" {
		respondValidation(c, msg)
		return
	}

	ctx := c.Request.Context()
	if !h.allow(c, "login:ip:"+c.ClientIP()) || !h.allow(c, "login:acct:"+req.Email) {
		return
	}

	id, err := h.authn.AuthenticateUser(ctx, domain.Credentials{Email: req.Email, Password: req.Password})
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCredentials) {
			respondError(c, domain.ErrInvalidCredentials)
			return
		}
		respondError(c, err)
		return
	}

	sid, err := h.session.Create(ctx, id)
	if err != nil {
		respondError(c, err)
		return
	}
	setSessionCookie(c, h.cookie, sid)
	csrf, err := setCSRFCookie(c, h.cookie)
	if err != nil {
		respondError(c, err)
		return
	}
	c.JSON(http.StatusOK, loginResponse{identityResponse: toIdentityResponse(id), CSRFToken: csrf})
}

// Logout revokes the current session and clears cookies. Session/cookie flow —
// not part of the public API docs.
func (h *AuthHandler) Logout(c *gin.Context) {
	ctx := c.Request.Context()
	if cookie, err := c.Request.Cookie(h.cookie.SessionName); err == nil && cookie.Value != "" {
		if err := h.session.Revoke(ctx, cookie.Value); err != nil {
			respondError(c, err)
			return
		}
	}
	clearSessionCookie(c, h.cookie)
	clearCSRFCookie(c, h.cookie)
	c.Status(http.StatusNoContent)
}

// Me returns the current identity.
//
// @Summary  Current identity
// @Tags     auth
// @Security CookieAuth
// @Security BearerAuth
// @Produce  json
// @Success  200  {object}  identityResponse
// @Failure  401  {object}  errorResponse
// @Router   /v1/auth/me [get]
func (h *AuthHandler) Me(c *gin.Context) {
	id, ok := mustIdentity(c)
	if !ok {
		respondError(c, domain.ErrUnauthenticated)
		return
	}
	c.JSON(http.StatusOK, toIdentityResponse(id))
}

// allow checks the rate limiter and, when blocked, writes a 429. Returns false
// when the request should stop.
func (h *AuthHandler) allow(c *gin.Context, key string) bool {
	if h.limiter == nil {
		return true
	}
	ok, retryAfter, err := h.limiter.AllowAttempt(c.Request.Context(), key)
	if err != nil {
		// Fail open on limiter errors, but log — availability over strictness here.
		logging.FromContext(c.Request.Context()).Warn("rate limiter error", logging.Err(err))
		return true
	}
	if !ok {
		c.Header(httpconst.HeaderRetryAfter, strconv.Itoa(int(retryAfter.Seconds())))
		c.AbortWithStatusJSON(http.StatusTooManyRequests,
			errorResponse{Error: errCodeRateLimited, Message: "too many attempts, please retry later"})
		return false
	}
	return true
}
