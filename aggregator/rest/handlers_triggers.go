package rest

import "github.com/labstack/echo/v4"

// Triggers resource — see api/openapi.yaml `tags: [Triggers]`.

// RunTrigger — POST /api/v1/triggers:run
//
// Evaluate a trigger definition against inline input. SDK testing flows
// use this to confirm a trigger config parses and returns the expected
// shape before committing it to a workflow.
func (s *Server) RunTrigger(ctx echo.Context) error {
	return s.notImplemented(ctx, "triggers.run")
}
