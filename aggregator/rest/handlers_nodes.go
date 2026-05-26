package rest

import "github.com/labstack/echo/v4"

// Nodes resource — see api/openapi.yaml `tags: [Nodes]`.

// RunNode — POST /api/v1/nodes:run
//
// Execute a single node definition against inline input variables
// without persisting a workflow. Used by SDK testing flows and the
// agent-CLI verify command.
func (s *Server) RunNode(ctx echo.Context) error {
	return s.notImplemented(ctx, "nodes.run")
}
