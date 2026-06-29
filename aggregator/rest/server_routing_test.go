package rest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestColonActionRouting verifies every `:verb` URL from api/openapi.yaml
// hits its own handler — both the item-level cases (`/workflows/:id:pause`)
// and the collection-level cases (`/workflows:simulate` vs
// `/workflows:estimateFees`). Echo's radix tree collapses two routes that
// share the same `<resource>:` prefix on the same HTTP method into one
// node; without rewriteColonActions + the shim registrations they'd alias
// to a single handler and SDK requests to the loser would silently invoke
// the winner. This test exercises the entire middleware-plus-shim stack
// in isolation, no engine wiring required.
func TestColonActionRouting(t *testing.T) {
	e := echo.New()
	e.Pre(rewriteColonActions)
	api := e.Group("/api/v1")

	// Replay every colon-suffix path the generated router would register.
	// Each handler echoes a unique tag so we can verify "the right one ran".
	// The filtering router drops the ones that need rewriting; the shim
	// re-registers them under the `__action__` path.
	routes := []struct {
		method string
		path   string
		tag    string
	}{
		// Item-level
		{"POST", "/workflows/:id:pause", "WORKFLOW_PAUSE"},
		{"POST", "/workflows/:id:resume", "WORKFLOW_RESUME"},
		{"POST", "/workflows/:id:trigger", "WORKFLOW_TRIGGER"},
		{"POST", "/wallets/:address:withdraw", "WALLET_WITHDRAW"},
		{"GET", "/wallets/:address:getNonce", "WALLET_GET_NONCE"},
		{"GET", "/executions/:id:getStatus", "EXECUTION_GET_STATUS"},
		{"POST", "/executions/:id:signal", "EXECUTION_SIGNAL"},
		{"GET", "/executions/:id:stream", "EXECUTION_STREAM"},
		// Collection-level — these are the ones the original bug missed
		{"POST", "/auth:exchange", "AUTH_EXCHANGE"},
		{"POST", "/workflows:simulate", "WORKFLOW_SIMULATE"},
		{"POST", "/workflows:estimateFees", "WORKFLOW_ESTIMATE_FEES"},
		{"GET", "/workflows:count", "WORKFLOW_COUNT"},
		{"GET", "/executions:count", "EXECUTION_COUNT"},
		{"GET", "/executions:stats", "EXECUTION_STATS"},
		{"POST", "/nodes:run", "NODE_RUN"},
		{"POST", "/triggers:run", "TRIGGER_RUN"},
		// Non-colliding regulars (left untouched by the shim)
		{"GET", "/workflows", "WORKFLOW_LIST"},
		{"GET", "/workflows/:id", "WORKFLOW_GET"},
		{"GET", "/executions", "EXECUTION_LIST"},
		{"GET", "/executions/:id", "EXECUTION_GET"},
		{"GET", "/health", "HEALTH"},
	}

	tagHandler := func(tag string) echo.HandlerFunc {
		return func(c echo.Context) error {
			return c.String(http.StatusOK, tag)
		}
	}

	// Stage 1: register every route through filteringRouter, mirroring how
	// the generated registration runs in production. Colliding paths get
	// dropped here.
	fr := filteringRouter{Group: api}
	registered := 0
	for _, r := range routes {
		switch r.method {
		case "GET":
			if out := fr.GET(r.path, tagHandler(r.tag)); out != nil {
				registered++
			}
		case "POST":
			if out := fr.POST(r.path, tagHandler(r.tag)); out != nil {
				registered++
			}
		}
	}
	// All non-colliding routes survive the filter. The 15 colon-suffix
	// routes (7 item + 8 collection) get dropped and re-registered below.
	assert.Equal(t, 5, registered, "non-colon routes should pass the filter unchanged")

	// Stage 2: register the shim routes under the disambiguated path. We
	// mirror the production set in registerColonActionShimRoutes; the tags
	// match the originals so a successful rewrite + lookup returns the
	// same string as if the colon URL had routed directly.
	for _, r := range routes {
		if !strings.Contains(r.path, ":") || !shouldDropRoute(r.path) {
			continue
		}
		shimPath := rewriteColonPathForTest(r.path)
		switch r.method {
		case "GET":
			api.GET(shimPath, tagHandler(r.tag))
		case "POST":
			api.POST(shimPath, tagHandler(r.tag))
		}
	}

	// Stage 3: exercise every URI exactly as a client would send it and
	// verify the right handler answered. URLs with `:id` / `:address`
	// placeholders get a concrete value substituted in.
	for _, r := range routes {
		uri := concreteURI(r.path)
		req := httptest.NewRequest(r.method, "/api/v1"+uri, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equalf(t, http.StatusOK, rec.Code,
			"%s %s should route — got %d, body=%q", r.method, uri, rec.Code, rec.Body.String())
		assert.Equalf(t, r.tag, rec.Body.String(),
			"%s %s routed to the wrong handler — Echo collision regression?", r.method, uri)
	}
}

// rewriteColonPathForTest computes the static shim path used to register
// a colon-suffix route. The action verb is always the final `:`-separated
// token, even when an earlier `:id` parametric segment is present. So
// `/workflows/:id:pause` lands on `/workflows/:id/__action__/pause` and
// `/workflows:simulate` lands on `/workflows/__action__/simulate`. The
// Pre middleware does the matching substitution on inbound URIs at
// request time — see rewriteColonActions.
func rewriteColonPathForTest(path string) string {
	colon := strings.LastIndex(path, ":")
	return path[:colon] + colonActionShim + path[colon+1:]
}

// concreteURI substitutes parametric segments with a sample value so the
// router has a non-empty path parameter to bind. Echo otherwise refuses
// to match an empty segment for a `:id` placeholder.
func concreteURI(specPath string) string {
	out := specPath
	out = strings.ReplaceAll(out, ":id", "01TEST00000000000000000000")
	out = strings.ReplaceAll(out, ":address", "0x0000000000000000000000000000000000000000")
	return out
}

// TestShouldDropRoute spot-checks the filter directly so a future edit
// to the route table can't silently re-introduce the collision.
func TestShouldDropRoute(t *testing.T) {
	cases := []struct {
		path string
		drop bool
	}{
		{"/health", false},
		{"/workflows", false},
		{"/workflows/:id", false},
		{"/auth:exchange", true},
		{"/workflows:simulate", true},
		{"/workflows:estimateFees", true},
		{"/workflows:count", true},
		{"/workflows/:id:pause", true},
		{"/wallets/:address:withdraw", true},
		{"/nodes:run", true},
	}
	for _, tc := range cases {
		assert.Equalf(t, tc.drop, shouldDropRoute(tc.path),
			"shouldDropRoute(%q)", tc.path)
	}
}
