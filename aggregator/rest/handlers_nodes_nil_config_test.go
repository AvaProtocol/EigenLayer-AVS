package rest

import (
	"crypto/ed25519"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
)

// runNodeCtx builds the Echo context POST /api/v1/nodes:run would arrive with
// after the JWT middleware ran — an authenticated user and a JSON body. The
// engine is never reached for these cases: node mapping fails first.
func runNodeCtx(body string) echo.Context {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/nodes:run", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c := e.NewContext(req, httptest.NewRecorder())
	c.Set("auth.user", &restmw.AuthenticatedUser{Subject: testPartnerSubject})
	return c
}

// A node posted with `"config": null` must come back as a 400 with a
// NODES_BAD_NODE code. It used to panic inside OpenAPIToProtoNode; the panic
// was caught by Echo's recover middleware and re-surfaced as a 500, which is
// what Sentry recorded as EIGENLAYER-AVS-29 (the panic) plus the generic
// "REST handler error" 5xx log line (EIGENLAYER-AVS-2A / -1J).
//
// customCode and loop are the two variants whose mapping arm reads through
// the Config pointer directly; the rest already errored cleanly.
func TestRunNodeRejectsNilConfigWithoutPanicking(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(nil)
	s := newPartnerServer(t, pub, []string{scopeRead}, partnerStatusActive)

	cases := []struct {
		name string
		body string
	}{
		{"customCodeNullConfig", `{"node":{"id":"n1","type":"customCode","config":null}}`},
		{"customCodeMissingConfig", `{"node":{"id":"n1","type":"customCode"}}`},
		{"loopNullConfig", `{"node":{"id":"n1","type":"loop","config":null}}`},
		{"loopMissingConfig", `{"node":{"id":"n1","type":"loop"}}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("RunNode panicked on %s: %v", tc.body, r)
				}
			}()

			err := s.RunNode(runNodeCtx(tc.body))
			assertHTTPStatus(t, err, http.StatusBadRequest)
			if code := err.(*restmw.HTTPError).Code; code != "NODES_BAD_NODE" {
				t.Fatalf("expected NODES_BAD_NODE, got %s", code)
			}
		})
	}
}
