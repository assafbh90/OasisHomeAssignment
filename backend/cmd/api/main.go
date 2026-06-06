// Command api is the IdentityHub backend entrypoint. All wiring lives in
// internal/app (the composition root); this is a thin shell so the wiring stays
// testable.
package main

import (
	"log/slog"
	"os"

	_ "github.com/assafbh/identityhub/docs" // generated OpenAPI spec (swag init)
	"github.com/assafbh/identityhub/internal/app"
	"github.com/assafbh/identityhub/internal/logging"
)

// @title           IdentityHub API
// @version         1.0
// @description     Multi-tenant API to connect a Jira Cloud workspace (OAuth 3LO)
// @description     and file NHI finding tickets — from the UI and from a REST API
// @description     guarded by a machine API key.
// @BasePath        /
//
// @securityDefinitions.apikey  BearerAuth
// @in                          header
// @name                        Authorization
// @description                 Machine API key as "Bearer ih_pat_…" (issue one via POST /v1/tokens).
//
// @securityDefinitions.apikey  CookieAuth
// @in                          cookie
// @name                        ih_session
// @description                 Interactive session cookie (set by POST /v1/auth/login).
func main() {
	if err := app.Run(); err != nil {
		// Bootstrap logger; the DI logger may not exist yet if config failed.
		slog.Error("fatal", logging.Err(err))
		os.Exit(1)
	}
}
