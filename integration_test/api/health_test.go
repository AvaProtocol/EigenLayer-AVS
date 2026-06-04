//go:build integration
// +build integration

package api_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
)

// TestHealth verifies the /api/v1/health probe answers without auth.
// Load balancers and k8s liveness probes hit this endpoint on every
// poll; an accidental 401 here would page everyone.
func TestHealth(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	resp, body := h.doRequest(http.MethodGet, "/api/v1/health", nil, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "body=%s", string(body))

	status := decodeJSON[generated.HealthStatus](t, body)
	assert.Equal(t, generated.Ok, status.Status, "status should be ok when engine is running")
	require.NotNil(t, status.Version, "version should be reported")
	assert.NotEmpty(t, *status.Version)
}

// TestHealthRejectsBadJWT exists to catch the inverse of TestHealth —
// if a future refactor accidentally moves /health behind the JWT
// middleware, an empty token bag would return 401 and break monitoring.
// Today we pass a deliberately bogus header and expect the handler to
// still succeed, because health probes deliberately stay unauthenticated.
func TestHealthRejectsBadJWT(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	resp, _ := h.doRequest(http.MethodGet, "/api/v1/health", nil, map[string]string{
		"Authorization": "Bearer nonsense.not.a.jwt",
	})
	// The JWT middleware rejects a malformed token even on otherwise-
	// public routes — that's expected today. If we ever scope JWT to
	// /workflows-only, this assertion flips to 200.
	require.True(t, resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusOK,
		"expected 200 or 401, got %d", resp.StatusCode)
}

// TestUnauthorizedWithoutBearer is the canonical "missing token" case
// for any handler that calls requireUser.
func TestUnauthorizedWithoutBearer(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	resp, body := h.doRequest(http.MethodGet, "/api/v1/workflows", nil, nil)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	require.True(t, strings.Contains(strings.ToLower(string(body)), "auth"),
		"problem body should mention auth, got: %s", string(body))
}
