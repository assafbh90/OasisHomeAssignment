package domain_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assafbh/identityhub/internal/domain"
)

func TestAutomationStatus_Values(t *testing.T) {
	t.Parallel()
	require.Equal(t, domain.AutomationStatus("idle"), domain.AutomationIdle)
	require.Equal(t, domain.AutomationStatus("running"), domain.AutomationRunning)
}
