// Command api is the IdentityHub backend entrypoint. All wiring lives in
// internal/app (the composition root); this is a thin shell so the wiring stays
// testable.
package main

import (
	"log/slog"
	"os"

	"github.com/assafbh/identityhub/internal/app"
	"github.com/assafbh/identityhub/internal/logging"
)

func main() {
	if err := app.Run(); err != nil {
		// Bootstrap logger; the DI logger may not exist yet if config failed.
		slog.Error("fatal", logging.Err(err))
		os.Exit(1)
	}
}
