package creator

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSimulatorCorsConfigShouldRejectBrowserCredentialsByDefault(t *testing.T) {
	cfg := simulatorCorsConfig()

	require.Nil(t, cfg.AllowedOrigins)
	require.Equal(t, []string{"GET", "POST", "OPTIONS"}, cfg.AllowedMethods)
	require.Contains(t, cfg.AllowedHeaders, "Content-Type")
	require.False(t, cfg.AllowCredentials)
}
