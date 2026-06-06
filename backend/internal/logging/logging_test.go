package logging_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/logging"
)

func TestErr(t *testing.T) {
	t.Parallel()
	attr := logging.Err(errors.New("boom"))
	require.Equal(t, logging.KeyError, attr.Key)
	require.Equal(t, "boom", attr.Value.String())

	// nil error must not panic and yields an empty value.
	require.Empty(t, logging.Err(nil).Value.String())
}

func TestContextHelpers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Defaults when nothing is set.
	require.NotNil(t, logging.FromContext(ctx))
	require.Empty(t, logging.RequestIDFromContext(ctx))

	l := logging.New("dev", "info")
	ctx = logging.WithLogger(ctx, l)
	ctx = logging.WithRequestID(ctx, "req-1")
	require.Same(t, l, logging.FromContext(ctx))
	require.Equal(t, "req-1", logging.RequestIDFromContext(ctx))
}
