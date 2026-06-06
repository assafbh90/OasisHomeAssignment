// Package logging provides structured logging (slog) and request-ID plumbing.
//
// A single *slog.Logger is built at bootstrap and injected via DI; there are no
// package-global loggers beyond the bootstrap default. Request-scoped loggers
// carry a request_id so every line for a request is correlated.
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

type ctxKey int

const (
	loggerKey ctxKey = iota
	requestIDKey
)

// Canonical structured-log attribute keys. Using these everywhere keeps log
// fields consistent and queryable instead of ad-hoc strings per call site.
const (
	KeyError      = "error"
	KeyRequestID  = "request_id"
	KeyTenantID   = "tenant_id"
	KeyUserID     = "user_id"
	KeyAuthMethod = "auth_method"
	KeyMethod     = "method"
	KeyPath       = "path"
	KeyAddr       = "addr"
)

// Err returns a standard error attribute. Centralizing this keeps the key
// uniform and avoids logging a nil error as a misleading empty string.
func Err(err error) slog.Attr {
	if err == nil {
		return slog.String(KeyError, "")
	}
	return slog.String(KeyError, err.Error())
}

// New builds the application logger. JSON in prod, human-readable text in dev.
func New(env, level string) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if strings.EqualFold(env, "prod") {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

// WithLogger stores a logger in the context.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, l)
}

// FromContext returns the request-scoped logger, or the slog default.
func FromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok && l != nil {
		return l
	}
	return slog.Default()
}

// WithRequestID stores the request ID in the context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFromContext returns the request ID if present.
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}
