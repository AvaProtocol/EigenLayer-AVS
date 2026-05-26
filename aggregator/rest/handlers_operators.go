package rest

import "github.com/labstack/echo/v4"

// Operators resource — see api/openapi.yaml `tags: [Operators]`.

// ListOperators — GET /api/v1/operators
//
// Read-only monitoring endpoint. Returns each connected operator's
// supportedChainIds, capabilities, lastSeen, and version. Routing
// decisions still live inside the gateway; this is for dashboards and
// debugging multi-chain task routing.
func (s *Server) ListOperators(ctx echo.Context) error {
	return s.notImplemented(ctx, "operators.list")
}
