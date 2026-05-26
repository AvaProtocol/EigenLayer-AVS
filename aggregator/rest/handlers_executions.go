package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/mapping"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Executions resource — see api/openapi.yaml `tags: [Executions]`.
//
// Executions are stored under their workflow's chain-scoped key prefix
// in BadgerDB. The engine APIs all require a workflow ID to scope the
// lookup, so REST endpoints whose URL omits the workflow context
// (`GET /executions/{id}`, `:stream`) stay stubbed until a future commit
// adds a workflow-id query param or a global execution index.

// ListExecutions — GET /api/v1/executions
//
// The flat list endpoint requires a workflowId filter today because the
// engine doesn't yet support a global "list executions for owner"
// scan. Without workflowId we return 400 pointing the caller at the
// nested route that does the same thing more explicitly.
func (s *Server) ListExecutions(ctx echo.Context, params generated.ListExecutionsParams) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}
	if params.WorkflowId == nil || len(*params.WorkflowId) == 0 {
		return badRequest("EXECUTIONS_WORKFLOW_REQUIRED",
			"workflowId filter required",
			"GET /api/v1/executions currently requires at least one workflowId query param; use GET /api/v1/workflows/{id}/executions for the nested form.")
	}

	req := buildListExecutionsReq(*params.WorkflowId, params.After, params.Before, params.Limit)
	resp, err := s.engine.ListExecutions(user, req)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, executionsRespToOpenAPI(resp, (*params.WorkflowId)[0]))
}

// ListExecutionsForWorkflow — GET /api/v1/workflows/{id}/executions
//
// Nested convenience route. The path parameter `id` pins the workflowId
// so the handler can hand the engine a single TaskIds entry.
func (s *Server) ListExecutionsForWorkflow(ctx echo.Context, id generated.Ulid, params generated.ListExecutionsForWorkflowParams) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	req := buildListExecutionsReq([]generated.Ulid{id}, params.After, params.Before, params.Limit)
	resp, err := s.engine.ListExecutions(user, req)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, executionsRespToOpenAPI(resp, string(id)))
}

// GetExecution — GET /api/v1/executions/{id}
//
// Standalone get is blocked on the same global-index gap as ListExecutions.
// Clients should fetch through the workflow route until that lands.
func (s *Server) GetExecution(ctx echo.Context, id generated.Ulid) error {
	return s.notImplemented(ctx, "executions.retrieve (standalone — use workflows-nested lookup)")
}

// GetExecutionStatus — GET /api/v1/executions/{id}:getStatus
//
// Lightweight summary endpoint; same workflow-scoping caveat as GetExecution.
func (s *Server) GetExecutionStatus(ctx echo.Context, id generated.Ulid) error {
	return s.notImplemented(ctx, "executions.getStatus (standalone)")
}

// StreamExecution — GET /api/v1/executions/{id}:stream
//
// SSE stream of ExecutionStatusSummary events. Implementation polls the
// engine at the configured interval and emits when status changes;
// closes when the execution reaches a terminal state.
func (s *Server) StreamExecution(ctx echo.Context, id generated.Ulid, params generated.StreamExecutionParams) error {
	return s.notImplemented(ctx, "executions.stream")
}

// CountExecutions — GET /api/v1/executions:count
func (s *Server) CountExecutions(ctx echo.Context, params generated.CountExecutionsParams) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	req := &avsproto.GetExecutionCountReq{}
	if params.WorkflowId != nil {
		for _, w := range *params.WorkflowId {
			req.WorkflowIds = append(req.WorkflowIds, string(w))
		}
	}
	resp, err := s.engine.GetExecutionCount(user, req)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, generated.ExecutionCount{Total: resp.GetTotal()})
}

// ExecutionStats — GET /api/v1/executions:stats
func (s *Server) ExecutionStats(ctx echo.Context, params generated.ExecutionStatsParams) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	req := &avsproto.GetExecutionStatsReq{}
	if params.WorkflowId != nil {
		for _, w := range *params.WorkflowId {
			req.WorkflowIds = append(req.WorkflowIds, string(w))
		}
	}
	resp, err := s.engine.GetExecutionStats(user, req)
	if err != nil {
		return err
	}

	avg := resp.GetAvgExecutionTime()
	out := generated.ExecutionStats{
		Total:            resp.GetTotal(),
		Succeeded:        resp.GetSucceeded(),
		Failed:           resp.GetFailed(),
		AvgExecutionTime: &avg,
	}
	return ctx.JSON(http.StatusOK, out)
}

// ---------------------------------------------------------------------
// Shared helpers — kept here while only the executions handlers use
// them; promote to a separate file if a second resource family needs
// the same shape.
// ---------------------------------------------------------------------

// buildListExecutionsReq assembles the engine request from the OpenAPI
// pagination params shared between the flat and nested list endpoints.
func buildListExecutionsReq(workflowIDs []generated.Ulid, after *generated.PageAfter, before *generated.PageBefore, limit *generated.PageLimit) *avsproto.ListExecutionsReq {
	req := &avsproto.ListExecutionsReq{}
	for _, w := range workflowIDs {
		req.TaskIds = append(req.TaskIds, string(w))
	}
	if after != nil {
		req.After = string(*after)
	}
	if before != nil {
		req.Before = string(*before)
	}
	if limit != nil {
		req.Limit = int64(*limit)
	}
	return req
}

// executionsRespToOpenAPI maps the engine's ListExecutionsResp to the
// OpenAPI ExecutionList envelope. Each execution's workflowId is taken
// from the supplied default — appropriate when listing for a single
// workflow; multi-workflow listings get the first id as the default and
// callers should not rely on it (the long-term fix is to thread the
// per-execution workflowId through from the storage key).
func executionsRespToOpenAPI(in *avsproto.ListExecutionsResp, defaultWorkflowID string) generated.ExecutionList {
	out := generated.ExecutionList{
		Data:     make([]generated.Execution, 0, len(in.GetItems())),
		PageInfo: protoPageInfoToOpenAPI(in.GetPageInfo()),
	}
	for _, item := range in.GetItems() {
		exec, err := mapping.ProtoToOpenAPIExecution(item, defaultWorkflowID)
		if err != nil {
			// A single bad execution should not break the whole page —
			// emit it with the minimum required fields and let the
			// caller inspect via the standalone retrieve endpoint.
			exec = generated.Execution{
				Id:         generated.Ulid(item.GetId()),
				WorkflowId: generated.Ulid(defaultWorkflowID),
				StartAt:    item.GetStartAt(),
				Status:     generated.ExecutionStatus("error"),
			}
		}
		out.Data = append(out.Data, exec)
	}
	return out
}
