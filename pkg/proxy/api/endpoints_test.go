package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGetMaxNumBlocksToGenerate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rawURL    string
		expected  int
		shouldErr bool
	}{
		{
			name:     "missing query uses default",
			rawURL:   "/generate",
			expected: maxNumOfBlockToGenerateUntilTxProcessed,
		},
		{
			name:     "valid query should work",
			rawURL:   "/generate?maxNumBlocks=7",
			expected: 7,
		},
		{
			name:      "non numeric query should error",
			rawURL:    "/generate?maxNumBlocks=invalid",
			shouldErr: true,
		},
		{
			name:      "negative query should error",
			rawURL:    "/generate?maxNumBlocks=-1",
			shouldErr: true,
		},
		{
			name:      "zero query should error",
			rawURL:    "/generate?maxNumBlocks=0",
			shouldErr: true,
		},
		{
			name:      "over cap query should error",
			rawURL:    "/generate?maxNumBlocks=21",
			shouldErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			context, _ := gin.CreateTestContext(httptest.NewRecorder())
			context.Request = httptest.NewRequest(http.MethodGet, tt.rawURL, nil)

			actual, err := getMaxNumBlocksToGenerate(context)
			if tt.shouldErr {
				require.Error(t, err)
				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.expected, actual)
		})
	}
}

func TestIsAllowedWebSocketOrigin(t *testing.T) {
	t.Parallel()

	t.Run("missing origin should work for non-browser clients", func(t *testing.T) {
		t.Parallel()

		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/log", nil)
		require.True(t, isAllowedWebSocketOrigin(request))
	})

	t.Run("same host origin should work", func(t *testing.T) {
		t.Parallel()

		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/log", nil)
		request.Header.Set("Origin", "http://127.0.0.1")
		require.True(t, isAllowedWebSocketOrigin(request))
	})

	t.Run("different host origin should fail", func(t *testing.T) {
		t.Parallel()

		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/log", nil)
		request.Header.Set("Origin", "http://evil.example")
		require.False(t, isAllowedWebSocketOrigin(request))
	})

	t.Run("malformed origin should fail", func(t *testing.T) {
		t.Parallel()

		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/log", nil)
		request.Header.Set("Origin", "http://[::1")
		require.False(t, isAllowedWebSocketOrigin(request))
	})
}
