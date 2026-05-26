package rest

import (
	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
)

// Executions resource — see api/openapi.yaml `tags: [Executions]`.

// ListExecutions — GET /api/v1/executions
func (s *Server) ListExecutions(ctx echo.Context, params generated.ListExecutionsParams) error {
	return s.notImplemented(ctx, "executions.list")
}

// ListExecutionsForWorkflow — GET /api/v1/workflows/{id}/executions
//
// Convenience nested route; shares the same handler logic as ListExecutions
// with `workflowId` pre-bound to the path parameter.
func (s *Server) ListExecutionsForWorkflow(ctx echo.Context, id generated.Ulid, params generated.ListExecutionsForWorkflowParams) error {
	return s.notImplemented(ctx, "executions.list (nested)")
}

// GetExecution — GET /api/v1/executions/{id}
func (s *Server) GetExecution(ctx echo.Context, id generated.Ulid) error {
	return s.notImplemented(ctx, "executions.retrieve")
}

// GetExecutionStatus — GET /api/v1/executions/{id}:getStatus
//
// Lightweight status — no steps payload. Used by clients that poll for
// terminal state without paying the cost of fetching the full execution
// record.
func (s *Server) GetExecutionStatus(ctx echo.Context, id generated.Ulid) error {
	return s.notImplemented(ctx, "executions.getStatus")
}

// StreamExecution — GET /api/v1/executions/{id}:stream
//
// SSE stream of ExecutionStatusSummary events. Implementation polls the
// DB at `interval` (default 1s) and emits when status changes; closes
// when the execution reaches a terminal state.
func (s *Server) StreamExecution(ctx echo.Context, id generated.Ulid, params generated.StreamExecutionParams) error {
	return s.notImplemented(ctx, "executions.stream")
}

// CountExecutions — GET /api/v1/executions:count
func (s *Server) CountExecutions(ctx echo.Context, params generated.CountExecutionsParams) error {
	return s.notImplemented(ctx, "executions.count")
}

// ExecutionStats — GET /api/v1/executions:stats
func (s *Server) ExecutionStats(ctx echo.Context, params generated.ExecutionStatsParams) error {
	return s.notImplemented(ctx, "executions.stats")
}
