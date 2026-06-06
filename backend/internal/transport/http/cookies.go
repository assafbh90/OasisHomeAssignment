package http

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/assafbh/identityhub/internal/secret"
)

// CookieConfig configures session/CSRF cookies.
type CookieConfig struct {
	SessionName string
	Secure      bool
	Domain      string
	MaxAge      time.Duration
}

// setSessionCookie writes the opaque session cookie. HttpOnly + SameSite=Lax so
// the OAuth callback navigation carries it while scripts cannot read it.
func setSessionCookie(c *gin.Context, cfg CookieConfig, value string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     cfg.SessionName,
		Value:    value,
		Path:     "/",
		Domain:   cfg.Domain,
		MaxAge:   int(cfg.MaxAge.Seconds()),
		Secure:   cfg.Secure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookie expires the session cookie.
func clearSessionCookie(c *gin.Context, cfg CookieConfig) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     cfg.SessionName,
		Value:    "",
		Path:     "/",
		Domain:   cfg.Domain,
		MaxAge:   -1,
		Secure:   cfg.Secure,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// setCSRFCookie writes the double-submit CSRF cookie. It is readable by the SPA
// (HttpOnly=false) so it can echo the value in the X-CSRF-Token header.
func setCSRFCookie(c *gin.Context, cfg CookieConfig) (string, error) {
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     csrfCookieName,
		Value:    token,
		Path:     "/",
		Domain:   cfg.Domain,
		MaxAge:   int(cfg.MaxAge.Seconds()),
		Secure:   cfg.Secure,
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
	})
	return token, nil
}

func clearCSRFCookie(c *gin.Context, cfg CookieConfig) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     csrfCookieName,
		Value:    "",
		Path:     "/",
		Domain:   cfg.Domain,
		MaxAge:   -1,
		Secure:   cfg.Secure,
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
	})
}

func randomToken() (string, error) {
	return secret.NewToken(secret.TokenBytes)
}
