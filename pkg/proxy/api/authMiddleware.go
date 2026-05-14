package api

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
)

// SimulatorAuthTokenEnv is the environment variable consulted at
// middleware construction time. If set to a non-empty value, every
// request to the simulator (regardless of HTTP method) must carry an
//
//	Authorization: Bearer <token>
//
// header where <token> matches the env-var value via constant-time
// comparison. If unset/empty, the middleware is a no-op (preserves
// the historical zero-auth behavior so existing dev/CI workflows that
// rely on loopback-bind safety continue to work).
//
// ISSUE-004 layer 2. Layer 1 (bind-safety) lives in
// cmd/chainsimulator/main.go (see --rest-api-interface +
// --unsafe-allow-public-bind). The two layers compose: a hardened
// deployment (public bind + auth token) is safe; the default
// deployment (loopback + no token) is safe; a misconfiguration
// (public bind + no token) is gated by the loud warning at the
// main.go bind-check site.
//
// HISTORICAL NOTE: an earlier revision exempted GET/HEAD/OPTIONS as
// "safe methods". That was unsound — GET /simulator/initial-wallets
// returns the chain-simulator's initial wallet keys (see
// node/chainSimulator/dtos/keys.go: WalletKey.PrivateKeyHex), so the
// "safe method = read-only = harmless" assumption did not hold for
// this endpoint. The exemption is removed; when auth is enabled,
// every method is gated, and CI tooling that calls the simulator
// must send the Bearer token on GETs as well as POSTs.
const SimulatorAuthTokenEnv = "MX_CHAIN_SIMULATOR_AUTH_TOKEN"

const bearerPrefix = "Bearer "

// IsSimulatorAuthEnabled returns true when the auth token env var is
// set to a non-empty value at the time of the call. Used by main.go
// to decide whether the public-bind warning should escalate to a
// "no token configured" hard-stop.
func IsSimulatorAuthEnabled() bool {
	return strings.TrimSpace(os.Getenv(SimulatorAuthTokenEnv)) != ""
}

// newAuthMiddleware builds the gin.HandlerFunc.
//
// Captures the token at construction time. Restart the simulator to
// rotate the token — this is intentional; a server that re-reads the
// env var per request would invite TOCTOU between the env-set and
// the request, and gives no observable benefit because the env is a
// process-lifetime secret.
//
// Constant-time compare via crypto/subtle to avoid a token-length
// timing oracle. (Realistic threat is low for a loopback service,
// but this is defense in depth and the cost is one allocation per
// request.)
//
// All methods are gated when the env var is set. See the
// SimulatorAuthTokenEnv doc-comment for the historical-note on why
// the previous safe-method exemption was removed.
func newAuthMiddleware() gin.HandlerFunc {
	expected := strings.TrimSpace(os.Getenv(SimulatorAuthTokenEnv))
	if expected == "" {
		// No-op middleware. Returning a real gin.HandlerFunc that just
		// calls c.Next() is cheaper than wrapping conditionally at
		// every route and keeps the registration code in
		// ExtendProxyServer uniform.
		return func(c *gin.Context) {
			c.Next()
		}
	}

	expectedBytes := []byte(expected)

	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if !strings.HasPrefix(authHeader, bearerPrefix) {
			respondAuthFailure(c, "missing or malformed Authorization header (expected: Bearer <token>)")
			return
		}

		presented := []byte(strings.TrimPrefix(authHeader, bearerPrefix))
		if subtle.ConstantTimeCompare(presented, expectedBytes) != 1 {
			respondAuthFailure(c, "invalid Bearer token")
			return
		}

		c.Next()
	}
}

func respondAuthFailure(c *gin.Context, message string) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
		"error": message,
		"code":  "simulator_auth_required",
	})
}
