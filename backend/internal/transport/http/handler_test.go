package http

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/domain"
	"github.com/assafbh/identityhub/internal/logging"
)

func init() { gin.SetMode(gin.TestMode) }

// --- fakes ---

type fakeResolver struct {
	id  domain.Identity
	err error
}

func (f fakeResolver) ResolveIdentity(context.Context, *http.Request) (domain.Identity, error) {
	return f.id, f.err
}

type fakeAuthn struct {
	id  domain.Identity
	err error
}

func (f fakeAuthn) AuthenticateUser(context.Context, domain.Credentials) (domain.Identity, error) {
	return f.id, f.err
}

type fakeSession struct {
	sid     string
	revoked []string
}

func (f *fakeSession) Create(context.Context, domain.Identity) (string, error) { return f.sid, nil }
func (f *fakeSession) Revoke(_ context.Context, sid string) error {
	f.revoked = append(f.revoked, sid)
	return nil
}

type fakeLimiter struct{ allow bool }

func (f fakeLimiter) AllowAttempt(context.Context, string) (bool, time.Duration, error) {
	return f.allow, time.Minute, nil
}

type fakeTokens struct {
	issued  bool
	revoked bool
}

func (f *fakeTokens) IssueToken(_ context.Context, owner domain.Identity, name string, scopes []string, _ *time.Time) (string, domain.TokenMeta, error) {
	f.issued = true
	return "ih_pat_secret", domain.TokenMeta{ID: uuid.New(), OwnerID: owner.UserID, TenantID: owner.TenantID, Name: name, Prefix: "ih_pat", Scopes: scopes, CreatedAt: time.Now()}, nil
}

func (f *fakeTokens) ListTokens(context.Context, domain.Identity) ([]domain.TokenMeta, error) {
	now := time.Now()
	return []domain.TokenMeta{{
		ID: uuid.New(), Name: "ci", Prefix: "ih_pat", Scopes: []string{"integrations:write"},
		ExpiresAt: &now, LastUsedAt: &now, CreatedAt: now,
	}}, nil
}

func (f *fakeTokens) RevokeToken(context.Context, domain.Identity, uuid.UUID) error {
	f.revoked = true
	return nil
}

func testCookie() CookieConfig {
	return CookieConfig{SessionName: "ih_session", Secure: false, MaxAge: time.Hour}
}

func newRouter(d RouterDeps) *gin.Engine {
	if d.Logger == nil {
		d.Logger = logging.New("dev", "error")
	}
	if d.Health == nil {
		d.Health = NewHealthHandler(okPinger{}, okPinger{})
	}
	return NewRouter(d)
}

type okPinger struct{}

func (okPinger) Ping(context.Context) error { return nil }

func doJSON(r http.Handler, method, path string, body any, cookies []*http.Cookie, headers map[string]string) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// --- tests ---

func TestLogin(t *testing.T) {
	id := domain.Identity{UserID: uuid.New(), TenantID: uuid.New(), AuthMethod: domain.AuthMethodSession}

	tests := []struct {
		name       string
		body       any
		authn      fakeAuthn
		limiter    rateLimiter
		wantStatus int
	}{
		{
			name:       "success",
			body:       loginRequest{Email: "a@b.com", Password: "pw"},
			authn:      fakeAuthn{id: id},
			limiter:    fakeLimiter{allow: true},
			wantStatus: http.StatusOK,
		},
		{
			name:       "invalid credentials",
			body:       loginRequest{Email: "a@b.com", Password: "bad"},
			authn:      fakeAuthn{err: domain.ErrInvalidCredentials},
			limiter:    fakeLimiter{allow: true},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "validation error",
			body:       loginRequest{Email: "", Password: ""},
			limiter:    fakeLimiter{allow: true},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "rate limited",
			body:       loginRequest{Email: "a@b.com", Password: "pw"},
			authn:      fakeAuthn{id: id},
			limiter:    fakeLimiter{allow: false},
			wantStatus: http.StatusTooManyRequests,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			authH := NewAuthHandler(tc.authn, &fakeSession{sid: "sid"}, tc.limiter, testCookie())
			r := newRouter(RouterDeps{Resolver: fakeResolver{}, Auth: authH, Tokens: NewTokenHandler(&fakeTokens{})})

			rec := doJSON(r, http.MethodPost, "/v1/auth/login", tc.body, nil, nil)

			require.Equal(t, tc.wantStatus, rec.Code, rec.Body.String())
			if tc.wantStatus == http.StatusOK {
				require.Contains(t, rec.Header().Get("Set-Cookie"), "ih_session=")
				var resp loginResponse
				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
				require.NotEmpty(t, resp.CSRFToken)
				require.Equal(t, id.UserID.String(), resp.UserID)
			}
		})
	}
}

func TestMe(t *testing.T) {
	id := domain.Identity{UserID: uuid.New(), TenantID: uuid.New(), AuthMethod: domain.AuthMethodSession}
	authH := NewAuthHandler(fakeAuthn{}, &fakeSession{}, fakeLimiter{allow: true}, testCookie())

	t.Run("authenticated", func(t *testing.T) {
		r := newRouter(RouterDeps{Resolver: fakeResolver{id: id}, Auth: authH, Tokens: NewTokenHandler(&fakeTokens{})})
		rec := doJSON(r, http.MethodGet, "/v1/auth/me", nil, nil, nil)
		require.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("unauthenticated", func(t *testing.T) {
		r := newRouter(RouterDeps{Resolver: fakeResolver{err: domain.ErrUnauthenticated}, Auth: authH, Tokens: NewTokenHandler(&fakeTokens{})})
		rec := doJSON(r, http.MethodGet, "/v1/auth/me", nil, nil, nil)
		require.Equal(t, http.StatusUnauthorized, rec.Code)
	})
}

func TestTokenIssue_SessionOnly(t *testing.T) {
	tokensH := NewTokenHandler(&fakeTokens{})
	authH := NewAuthHandler(fakeAuthn{}, &fakeSession{}, fakeLimiter{allow: true}, testCookie())

	body := issueTokenRequest{Name: "ci", Scopes: []string{domain.ScopeIntegrationsWrite}}
	csrf := []*http.Cookie{{Name: csrfCookieName, Value: "t"}}
	hdr := map[string]string{headerCSRFToken: "t"}

	t.Run("machine token rejected", func(t *testing.T) {
		id := domain.Identity{UserID: uuid.New(), TenantID: uuid.New(), AuthMethod: domain.AuthMethodToken}
		r := newRouter(RouterDeps{Resolver: fakeResolver{id: id}, Auth: authH, Tokens: tokensH})
		rec := doJSON(r, http.MethodPost, "/v1/tokens", body, csrf, hdr)
		require.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("session accepted", func(t *testing.T) {
		id := domain.Identity{UserID: uuid.New(), TenantID: uuid.New(), AuthMethod: domain.AuthMethodSession}
		r := newRouter(RouterDeps{Resolver: fakeResolver{id: id}, Auth: authH, Tokens: tokensH})
		rec := doJSON(r, http.MethodPost, "/v1/tokens", body, csrf, hdr)
		require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	})
}

func TestCSRF_Logout(t *testing.T) {
	id := domain.Identity{UserID: uuid.New(), TenantID: uuid.New(), AuthMethod: domain.AuthMethodSession}
	sess := &fakeSession{}
	authH := NewAuthHandler(fakeAuthn{}, sess, fakeLimiter{allow: true}, testCookie())
	r := newRouter(RouterDeps{Resolver: fakeResolver{id: id}, Auth: authH, Tokens: NewTokenHandler(&fakeTokens{})})

	sessionCookie := &http.Cookie{Name: "ih_session", Value: "sid"}
	csrfCookie := &http.Cookie{Name: csrfCookieName, Value: "tok"}

	t.Run("missing csrf header rejected", func(t *testing.T) {
		rec := doJSON(r, http.MethodPost, "/v1/auth/logout", nil, []*http.Cookie{sessionCookie, csrfCookie}, nil)
		require.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("valid csrf header accepted", func(t *testing.T) {
		rec := doJSON(r, http.MethodPost, "/v1/auth/logout", nil,
			[]*http.Cookie{sessionCookie, csrfCookie}, map[string]string{headerCSRFToken: "tok"})
		require.Equal(t, http.StatusNoContent, rec.Code)
		require.Contains(t, sess.revoked, "sid")
	})
}
