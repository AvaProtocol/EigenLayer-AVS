package rest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/mapping"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// streamMaxDuration caps the SSE stream lifetime so a runaway client
// can't pin a goroutine indefinitely. Production callers are expected
// to reconnect when the stream closes; the OpenAPI documents the
// behavior.
const streamMaxDuration = 10 * time.Minute

// streamMinInterval is the floor for the configurable poll interval.
// Without it a `?interval=1ms` client would DOS the engine's GetExecution
// loop.
const streamMinInterval = 250 * time.Millisecond

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

// GetExecution — GET /api/v1/executions/{id}?workflowId=...
//
// The standalone route requires `workflowId` because executions are
// stored under `t:<chain>:<workflow_id>:<exec_id>` — there is no
// global index. Callers that already know the workflow can use the
// nested form (`GET /workflows/{id}/executions`) too.
func (s *Server) GetExecution(ctx echo.Context, id generated.Ulid, params generated.GetExecutionParams) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}
	workflowID := string(params.WorkflowId)
	exec, err := s.engine.GetExecution(user, &avsproto.ExecutionReq{
		TaskId:      workflowID,
		ExecutionId: string(id),
	})
	if err != nil {
		return notFoundOrError(err)
	}
	resp, err := mapping.ProtoToOpenAPIExecution(exec, workflowID)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, resp)
}

// SignalExecution — POST /api/v1/executions/{id}:signal
//
// Delivers an approval/external signal to a WAITING execution (durable execution).
// The caller must own the workflow; the engine's gate (DeliverSignal) rejects a
// signal with no pending wait, a mismatched kind, or one that has timed out.
func (s *Server) SignalExecution(ctx echo.Context, id generated.Ulid, params generated.SignalExecutionParams) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}
	var body generated.SignalExecutionRequest
	if err := ctx.Bind(&body); err != nil {
		return badRequest("SIGNAL_BAD_REQUEST", "Invalid request body", err.Error())
	}
	var payload *structpb.Value
	if body.Payload != nil {
		p, perr := structpb.NewValue(map[string]interface{}(*body.Payload))
		if perr != nil {
			return badRequest("SIGNAL_BAD_PAYLOAD", "Invalid payload", perr.Error())
		}
		payload = p
	}
	workflowID := string(params.WorkflowId)
	exec, err := s.engine.SignalExecution(user, workflowID, string(id), string(body.Decision), payload)
	if err != nil {
		return notFoundOrError(err)
	}
	resp, err := mapping.ProtoToOpenAPIExecution(exec, workflowID)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, resp)
}

// GetExecutionStatus — GET /api/v1/executions/{id}:getStatus?workflowId=...
//
// Lightweight summary — same scoping rule as GetExecution. Used by
// clients polling for terminal state without paying the cost of
// fetching the full execution record.
func (s *Server) GetExecutionStatus(ctx echo.Context, id generated.Ulid, params generated.GetExecutionStatusParams) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}
	workflowID := string(params.WorkflowId)
	statusResp, err := s.engine.GetExecutionStatus(user, &avsproto.ExecutionReq{
		TaskId:      workflowID,
		ExecutionId: string(id),
	})
	if err != nil {
		return notFoundOrError(err)
	}
	// engine.GetExecutionStatus returns just the enum; lift it into the
	// summary envelope the OpenAPI schema documents. Start/end timestamps
	// require the full Execution, which a polling caller can fetch on
	// the terminal state via GetExecution.
	out := generated.ExecutionStatusSummary{
		Id:     id,
		Status: generated.ExecutionStatus(executionStatusToWire(statusResp.GetStatus())),
	}
	wid := generated.Ulid(workflowID)
	out.WorkflowId = &wid
	return ctx.JSON(http.StatusOK, out)
}

// StreamExecution — GET /api/v1/executions/{id}:stream?workflowId=...
//
// SSE stream of ExecutionStatusSummary events. Polls engine.GetExecution
// on the configured interval (clamped to streamMinInterval) and emits
// when the status changes. Closes on terminal status, after
// streamMaxDuration, or when the client disconnects.
func (s *Server) StreamExecution(ctx echo.Context, id generated.Ulid, params generated.StreamExecutionParams) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}
	workflowID := string(params.WorkflowId)

	interval := time.Second
	if params.Interval != nil && *params.Interval != "" {
		if parsed, perr := time.ParseDuration(*params.Interval); perr == nil && parsed > 0 {
			interval = parsed
		}
	}
	if interval < streamMinInterval {
		interval = streamMinInterval
	}

	w := ctx.Response().Writer
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx/Cloudflare buffering
	ctx.Response().WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming unsupported: response writer is not a Flusher")
	}

	clientGone := ctx.Request().Context().Done()
	deadline := time.After(streamMaxDuration)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	emit := func(status string, exec *avsproto.Execution) error {
		summary := generated.ExecutionStatusSummary{
			Id:     id,
			Status: generated.ExecutionStatus(status),
		}
		wid := generated.Ulid(workflowID)
		summary.WorkflowId = &wid
		if exec != nil {
			if v := exec.GetStartAt(); v != 0 {
				summary.StartAt = &v
			}
			if v := exec.GetEndAt(); v != 0 {
				summary.EndAt = &v
			}
			if msg := exec.GetError(); msg != "" {
				summary.Error = &msg
			}
		}
		raw, err := json.Marshal(summary)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", raw); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}

	lastStatus := ""
	poll := func() (bool, error) {
		// GetExecution returns the full record once the execution has
		// reached a terminal state; before that it returns a pending
		// stub via the queue path.
		exec, err := s.engine.GetExecution(user, &avsproto.ExecutionReq{
			TaskId:      workflowID,
			ExecutionId: string(id),
		})
		if err != nil {
			// Execution may not exist yet (created lazily by the
			// trigger). Send a `pending` heartbeat so the client knows
			// the stream is alive.
			if lastStatus != "pending" {
				if emitErr := emit("pending", nil); emitErr != nil {
					return true, emitErr
				}
				lastStatus = "pending"
			}
			return false, nil
		}
		current := executionStatusToWire(exec.GetStatus())
		if current != lastStatus {
			if err := emit(current, exec); err != nil {
				return true, err
			}
			lastStatus = current
		}
		// Terminal states close the stream — see ExecutionStatus comment
		// in the OpenAPI schema.
		switch current {
		case "success", "failed", "error":
			return true, nil
		}
		return false, nil
	}

	// Initial probe so the client gets an immediate frame instead of
	// waiting one full interval.
	if done, err := poll(); err != nil {
		return err
	} else if done {
		return nil
	}

	for {
		select {
		case <-clientGone:
			return nil
		case <-deadline:
			return nil
		case <-ticker.C:
			if done, err := poll(); err != nil {
				return err
			} else if done {
				return nil
			}
		}
	}
}

// executionStatusToWire mirrors the helper in the workflows handler
// but lives here too because the executions package uses it on every
// poll and the workflows file is already large.
func executionStatusToWire(s avsproto.ExecutionStatus) string {
	switch s {
	case avsproto.ExecutionStatus_EXECUTION_STATUS_PENDING:
		return "pending"
	case avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS:
		return "success"
	case avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED:
		return "failed"
	case avsproto.ExecutionStatus_EXECUTION_STATUS_ERROR:
		return "error"
	default:
		return "pending"
	}
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
