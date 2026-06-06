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
