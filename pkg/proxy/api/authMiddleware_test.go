package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// helper: build a tiny gin engine with the auth middleware applied,
// then a single POST + GET handler so we can probe both methods.
func buildTestEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(newAuthMiddleware())
	r.POST("/mutate", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/observe", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

func TestAuthMiddleware_DisabledByDefault_AllowsAllMethods(t *testing.T) {
	t.Setenv(SimulatorAuthTokenEnv, "")
	r := buildTestEngine()

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		var path string
		if method == http.MethodGet {
			path = "/observe"
		} else {
			path = "/mutate"
		}
		req := httptest.NewRequest(method, path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code, "method %s should pass when env unset", method)
	}
}

func TestAuthMiddleware_EnabledRequiresBearerOnPOST(t *testing.T) {
	t.Setenv(SimulatorAuthTokenEnv, "secret-token-xyz")
	r := buildTestEngine()

	cases := []struct {
		name       string
		header     string
		wantStatus int
	}{
		{"no header at all", "", http.StatusUnauthorized},
		{"wrong scheme", "Basic Zm9vOmJhcg==", http.StatusUnauthorized},
		{"bearer with wrong token", "Bearer wrong", http.StatusUnauthorized},
		{"bearer with empty token", "Bearer ", http.StatusUnauthorized},
		{"correct bearer", "Bearer secret-token-xyz", http.StatusOK},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/mutate", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, tc.wantStatus, w.Code)
		})
	}
}

// Regression closure for the "safe-method exemption" bug: when auth
// is enabled, GET requests must also be gated. The previous behavior
// exempted GET/HEAD/OPTIONS, which exposed GET /simulator/initial-wallets
// (returning WalletKey.PrivateKeyHex) to unauthenticated callers.
func TestAuthMiddleware_EnabledGatesGETToo(t *testing.T) {
	t.Setenv(SimulatorAuthTokenEnv, "any-secret")
	r := buildTestEngine()

	// Without a Bearer header, every method must 401 — including GET.
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		req := httptest.NewRequest(method, "/observe", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code,
			"method %s must be auth-gated when env var is set (regression closure for initial-wallets leak)", method)
	}

	// With the correct Bearer header, GET passes through to the router.
	req := httptest.NewRequest(http.MethodGet, "/observe", nil)
	req.Header.Set("Authorization", "Bearer any-secret")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code, "GET with valid Bearer must pass through")
}

func TestAuthMiddleware_TrimsSurroundingWhitespaceOnEnvVar(t *testing.T) {
	// Operators sometimes paste tokens with trailing newlines from
	// secret managers; the env-var read must tolerate that without
	// silently producing an unguessable mismatch.
	t.Setenv(SimulatorAuthTokenEnv, "  trim-me  \n")
	r := buildTestEngine()

	req := httptest.NewRequest(http.MethodPost, "/mutate", nil)
	req.Header.Set("Authorization", "Bearer trim-me")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code,
		"env var with surrounding whitespace should match a clean Bearer token")
}

func TestIsSimulatorAuthEnabled(t *testing.T) {
	t.Setenv(SimulatorAuthTokenEnv, "")
	require.False(t, IsSimulatorAuthEnabled(), "empty env -> disabled")

	t.Setenv(SimulatorAuthTokenEnv, "   ")
	require.False(t, IsSimulatorAuthEnabled(), "whitespace-only env -> disabled")

	t.Setenv(SimulatorAuthTokenEnv, "real-secret")
	require.True(t, IsSimulatorAuthEnabled(), "non-empty env -> enabled")
}
