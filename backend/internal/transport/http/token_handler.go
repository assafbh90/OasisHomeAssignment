package http

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/samber/lo"

	"github.com/assafbh/identityhub/internal/domain"
)

// tokenSvc is the slice of apitoken.TokenService the handler needs.
type tokenSvc interface {
	IssueToken(ctx context.Context, owner domain.Identity, name string, scopes []string, expiresAt *time.Time) (string, domain.TokenMeta, error)
	ListTokens(ctx context.Context, owner domain.Identity) ([]domain.TokenMeta, error)
	RevokeToken(ctx context.Context, owner domain.Identity, tokenID uuid.UUID) error
}

// TokenHandler serves machine-API-key management.
type TokenHandler struct {
	tokens tokenSvc
}

// NewTokenHandler constructs the handler.
func NewTokenHandler(tokens tokenSvc) *TokenHandler {
	return &TokenHandler{tokens: tokens}
}

// Issue creates a new API key and returns the plaintext exactly once.
//
// @Summary  Issue an API key (plaintext shown once)
// @Tags     api-keys
// @Security CookieAuth
// @Accept   json
// @Produce  json
// @Param    request  body      issueTokenRequest  true  "Key name, scopes, optional expiry"
// @Success  201      {object}  issuedTokenResponse
// @Failure  400      {object}  errorResponse
// @Failure  401      {object}  errorResponse
// @Router   /v1/tokens [post]
func (h *TokenHandler) Issue(c *gin.Context) {
	id, _ := mustIdentity(c)
	var req issueTokenRequest
	if err := bindJSON(c, &req); err != nil {
		respondValidation(c, "invalid request body")
		return
	}
	if msg := req.validate(); msg != "" {
		respondValidation(c, msg)
		return
	}

	plaintext, meta, err := h.tokens.IssueToken(c.Request.Context(), id, req.Name, req.Scopes, req.ExpiresAt)
	if err != nil {
		respondError(c, err)
		return
	}

	c.JSON(http.StatusCreated, issuedTokenResponse{
		ID:        meta.ID.String(),
		Token:     plaintext,
		Name:      meta.Name,
		Prefix:    meta.Prefix,
		Scopes:    emptyIfNil(meta.Scopes),
		ExpiresAt: timePtr(meta.ExpiresAt),
		CreatedAt: meta.CreatedAt.UTC().Format(time.RFC3339),
	})
}

// List returns the caller's tokens (metadata only).
//
// @Summary  List API keys (metadata only)
// @Tags     api-keys
// @Security CookieAuth
// @Security BearerAuth
// @Produce  json
// @Success  200  {object}  map[string][]tokenMetaResponse
// @Failure  401  {object}  errorResponse
// @Router   /v1/tokens [get]
func (h *TokenHandler) List(c *gin.Context) {
	id, _ := mustIdentity(c)
	metas, err := h.tokens.ListTokens(c.Request.Context(), id)
	if err != nil {
		respondError(c, err)
		return
	}
	out := lo.Map(metas, func(m domain.TokenMeta, _ int) tokenMetaResponse {
		return toTokenMetaResponse(m)
	})
	c.JSON(http.StatusOK, gin.H{"tokens": out})
}

// Revoke revokes one of the caller's tokens.
//
// @Summary  Revoke an API key
// @Tags     api-keys
// @Security CookieAuth
// @Security BearerAuth
// @Param    id   path      string  true  "Token ID (UUID)"
// @Success  204
// @Failure  404  {object}  errorResponse
// @Router   /v1/tokens/{id} [delete]
func (h *TokenHandler) Revoke(c *gin.Context) {
	id, _ := mustIdentity(c)
	tokenID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		respondValidation(c, "invalid token id")
		return
	}
	if err := h.tokens.RevokeToken(c.Request.Context(), id, tokenID); err != nil {
		if errors.Is(err, domain.ErrTokenNotFound) {
			c.JSON(http.StatusNotFound, errorResponse{Error: errCodeNotFound, Message: "token not found"})
			return
		}
		respondError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
