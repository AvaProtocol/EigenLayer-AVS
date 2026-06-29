package rest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
)

// TestSignalColonRouteReachesHandler is the HTTP-level regression for the
// POST /executions/{id}:signal 404 the SDK hit: the generated colon-route is
// dropped by filteringRouter and must be re-registered in
// registerColonActionShimRoutes — which originally omitted :signal, so the
// rewritten path matched nothing. Unlike TestColonActionRouting (which replays
// its own shim list), this exercises the REAL registerColonActionShimRoutes, so
// a future colon-action added to the spec but not the shim fails here.
func TestSignalColonRouteReachesHandler(t *testing.T) {
	e := echo.New()
	e.Pre(rewriteColonActions)
	api := e.Group("/api/v1")
	registerColonActionShimRoutes(api, &Server{}) // closures aren't invoked at registration

	// The signal route must match (404 = the bug; any other code = route reached
	// its handler, which then fails auth/validation — not our concern here).
	req := httptest.NewRequest(http.MethodPost, "/api/v1/executions/abc:signal?workflowId=wf",
		strings.NewReader(`{"decision":"approve"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assert.NotEqualf(t, http.StatusNotFound, rec.Code,
		"POST /executions/{id}:signal must route to a handler; 404 means the shim is missing")

	// Control: an unregistered colon-verb correctly 404s — proves the check above
	// distinguishes "routed" from "not found".
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/executions/abc:bogusverb", nil)
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusNotFound, rec2.Code, "an unregistered colon-verb should 404")
}
