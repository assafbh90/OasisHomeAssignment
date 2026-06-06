package app

import (
	"context"
	"errors"
	"log/slog"
	"os"

	"github.com/assafbh/identityhub/internal/domain"
)

// Seed configuration env vars (override the local-dev defaults below).
const (
	envSeedOrgSlug      = "SEED_ORG_SLUG"
	envSeedOrgName      = "SEED_ORG_NAME"
	envSeedUserEmail    = "SEED_USER_EMAIL"
	envSeedUserPassword = "SEED_USER_PASSWORD"
)

// Seed creates demo data for a frictionless first run: an "acme" organization
// with one user. It is idempotent. Credentials are configurable via env so no
// password is hardcoded in a way that ships to prod; defaults are local-dev only
// and printed to the operator.
func (a *App) Seed(ctx context.Context) error {
	org := envOr(envSeedOrgSlug, "acme")
	orgName := envOr(envSeedOrgName, "Acme Corp")
	email := envOr(envSeedUserEmail, "admin@acme.test")
	password := envOr(envSeedUserPassword, "password123")

	tenant, err := a.tenants.FindBySlug(ctx, org)
	if err != nil {
		if !errors.Is(err, domain.ErrTenantNotFound) {
			return err
		}
		tenant, err = a.tenants.Create(ctx, org, orgName)
		if err != nil {
			return err
		}
		a.log.Info("seeded tenant", slog.String("slug", org))
	}

	if _, err := a.users.FindUserByEmail(ctx, tenant.ID, email); err == nil {
		a.log.Info("seed user already exists", slog.String("email", email))
		return nil
	} else if !errors.Is(err, domain.ErrUserNotFound) {
		return err
	}

	hash, err := a.hasher.HashPassword(ctx, password)
	if err != nil {
		return err
	}
	if _, err := a.users.CreateUser(ctx, tenant.ID, email, hash); err != nil {
		return err
	}
	a.log.Info("seeded user",
		slog.String("org", org), slog.String("email", email), slog.String("password", password))
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
