package httpconst_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/httpconst"
)

func TestIsSuccessStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		statusCode int
		want       bool
	}{
		{199, false}, // just below 2xx
		{200, true},  // lower bound
		{204, true},
		{299, true}, // upper bound
		{300, false},
		{404, false},
		{500, false},
	}
	for _, tc := range cases {
		require.Equal(t, tc.want, httpconst.IsSuccessStatus(tc.statusCode), "status %d", tc.statusCode)
	}
}
