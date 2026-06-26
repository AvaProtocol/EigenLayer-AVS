package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/mapping"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Workflows resource — see api/openapi.yaml `tags: [Workflows]`.
//
// Every handler is the same shape:
//
//	1. requireUser — pull the JWT subject from echo context.
//	2. (for writes) decode the request body and translate via mapping/.
//	3. Call the engine method.
//	4. Translate the response back via mapping/ (or shape a tiny envelope).
//
// The translator package keeps these files thin; the engine method does the
// actual work.

// CreateWorkflow — POST /api/v1/workflows
func (s *Server) CreateWorkflow(ctx echo.Context) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	var body generated.CreateWorkflowRequest
	if err := ctx.Bind(&body); err != nil {
		return badRequest("WORKFLOWS_BAD_REQUEST", "Invalid request body", err.Error())
	}

	req, err := mapping.OpenAPIToProtoCreateWorkflow(body)
	if err != nil {
		return badRequest("WORKFLOWS_BAD_PAYLOAD", "Invalid workflow payload", err.Error())
	}

	// A task no longer carries a workflow-level chain (G5) — each chain-aware
	// trigger/node specifies its own chain_id. The JWT audience chain is API
	// auth scope only; it is not stamped onto the task.

	workflow, err := s.engine.CreateWorkflow(user, req)
	if err != nil {
		return err
	}

	resp, err := mapping.ProtoToOpenAPIWorkflow(workflow.Task)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusCreated, resp)
}

// ListWorkflows — GET /api/v1/workflows
func (s *Server) ListWorkflows(ctx echo.Context, params generated.ListWorkflowsParams) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	req := &avsproto.ListTasksReq{
		IncludeNodes: true,
		IncludeEdges: true,
	}
	if params.SmartWalletAddress != nil {
		for _, addr := range *params.SmartWalletAddress {
			req.SmartWalletAddress = append(req.SmartWalletAddress, string(addr))
		}
	}
	if params.After != nil {
		req.After = string(*params.After)
	}
	if params.Before != nil {
		req.Before = string(*params.Before)
	}
	if params.Limit != nil {
		req.Limit = int64(*params.Limit)
	}

	listResp, err := s.engine.ListWorkflowsByUser(user, req)
	if err != nil {
		return err
	}

	data := make([]generated.Workflow, 0, len(listResp.GetItems()))
	for _, item := range listResp.GetItems() {
		w, err := mapping.ProtoToOpenAPIWorkflow(item)
		if err != nil {
			return err
		}
		data = append(data, w)
	}

	out := generated.WorkflowList{
		Data:     data,
		PageInfo: protoPageInfoToOpenAPI(listResp.GetPageInfo()),
	}
	return ctx.JSON(http.StatusOK, out)
}

// GetWorkflow — GET /api/v1/workflows/{id}
func (s *Server) GetWorkflow(ctx echo.Context, id generated.Ulid) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	workflow, err := s.engine.GetWorkflow(user, string(id))
	if err != nil {
		return notFoundOrError(err)
	}
	resp, err := mapping.ProtoToOpenAPIWorkflow(workflow.Task)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, resp)
}

// CancelWorkflow — DELETE /api/v1/workflows/{id}
//
// Deletes the workflow. Cancel semantics match Stripe — soft from the
// caller's perspective (idempotent, returns 204) but the engine removes
// the underlying record so the operator stops monitoring.
func (s *Server) CancelWorkflow(ctx echo.Context, id generated.Ulid) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	if _, err := s.engine.DeleteWorkflowByUser(user, string(id)); err != nil {
		return notFoundOrError(err)
	}
	return ctx.NoContent(http.StatusNoContent)
}

// PauseWorkflow — POST /api/v1/workflows/{id}:pause
func (s *Server) PauseWorkflow(ctx echo.Context, id generated.Ulid) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	if _, err := s.engine.SetWorkflowEnabledByUser(user, string(id), false); err != nil {
		return notFoundOrError(err)
	}
	// Return the refreshed workflow so the caller doesn't have to make a
	// second round-trip to see the new status.
	workflow, err := s.engine.GetWorkflow(user, string(id))
	if err != nil {
		return err
	}
	resp, err := mapping.ProtoToOpenAPIWorkflow(workflow.Task)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, resp)
}

// ResumeWorkflow — POST /api/v1/workflows/{id}:resume
func (s *Server) ResumeWorkflow(ctx echo.Context, id generated.Ulid) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	if _, err := s.engine.SetWorkflowEnabledByUser(user, string(id), true); err != nil {
		return notFoundOrError(err)
	}
	workflow, err := s.engine.GetWorkflow(user, string(id))
	if err != nil {
		return err
	}
	resp, err := mapping.ProtoToOpenAPIWorkflow(workflow.Task)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, resp)
}

// TriggerWorkflow — POST /api/v1/workflows/{id}:trigger
//
// Manually fires a workflow's trigger as though the operator had observed
// the configured condition. Used by the Studio "Run now" affordance and
// by SDK tests. The optional triggerOutput in the request body simulates
// what the operator would have reported; if omitted, the engine fills
// it with placeholder values.
func (s *Server) TriggerWorkflow(ctx echo.Context, id generated.Ulid) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	var body generated.TriggerWorkflowRequest
	if err := ctx.Bind(&body); err != nil {
		return badRequest("WORKFLOWS_BAD_REQUEST", "Invalid request body", err.Error())
	}

	triggerType, err := openAPITriggerTypeToProto(body.TriggerType)
	if err != nil {
		return badRequest("WORKFLOWS_BAD_TRIGGER_TYPE", "Unknown trigger type", err.Error())
	}

	req := &avsproto.TriggerTaskReq{
		TaskId:      string(id),
		TriggerType: triggerType,
	}
	if body.IsBlocking != nil {
		req.IsBlocking = *body.IsBlocking
	}
	if body.TriggerInput != nil {
		converted, err := openAPIInputVarsToProto(*body.TriggerInput)
		if err != nil {
			return badRequest("WORKFLOWS_BAD_TRIGGER_INPUT", "Invalid triggerInput payload", err.Error())
		}
		req.TriggerInput = converted
	}
	if body.TriggerOutput != nil {
		// The output oneof variants implement an unexported sealed
		// interface, so the switch lives inline rather than in a
		// helper.
		v, err := structpb.NewValue(*body.TriggerOutput)
		if err != nil {
			return badRequest("WORKFLOWS_BAD_TRIGGER_OUTPUT", "triggerOutput is not JSON-serializable", err.Error())
		}
		switch triggerType {
		case avsproto.TriggerType_TRIGGER_TYPE_BLOCK:
			req.TriggerOutput = &avsproto.TriggerTaskReq_BlockTrigger{BlockTrigger: &avsproto.BlockTrigger_Output{Data: v}}
		case avsproto.TriggerType_TRIGGER_TYPE_CRON:
			req.TriggerOutput = &avsproto.TriggerTaskReq_CronTrigger{CronTrigger: &avsproto.CronTrigger_Output{Data: v}}
		case avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME:
			req.TriggerOutput = &avsproto.TriggerTaskReq_FixedTimeTrigger{FixedTimeTrigger: &avsproto.FixedTimeTrigger_Output{Data: v}}
		case avsproto.TriggerType_TRIGGER_TYPE_EVENT:
			req.TriggerOutput = &avsproto.TriggerTaskReq_EventTrigger{EventTrigger: &avsproto.EventTrigger_Output{Data: v}}
		case avsproto.TriggerType_TRIGGER_TYPE_MANUAL:
			req.TriggerOutput = &avsproto.TriggerTaskReq_ManualTrigger{ManualTrigger: &avsproto.ManualTrigger_Output{Data: v}}
		}
	}

	resp, err := s.engine.TriggerWorkflowWithContext(ctx.Request().Context(), user, req)
	if err != nil {
		return notFoundOrError(err)
	}

	// The proto TriggerTaskResp shape closely mirrors the OpenAPI
	// response; lift it field-by-field rather than via the JSON
	// roundtrip helper because the proto Status field is an enum we
	// translate to a lowercase string.
	out := generated.TriggerWorkflowResponse{
		ExecutionId: generated.Ulid(resp.GetExecutionId()),
		Status:      generated.ExecutionStatus(protoExecStatusToOpenAPI(resp.GetStatus())),
	}
	if v := resp.GetStartAt(); v != 0 {
		out.StartAt = &v
	}
	if v := resp.GetEndAt(); v != 0 {
		out.EndAt = &v
	}
	if msg := resp.GetError(); msg != "" {
		out.Error = &msg
	}
	return ctx.JSON(http.StatusOK, out)
}

// SimulateWorkflow — POST /api/v1/workflows:simulate
//
// Runs the supplied trigger + nodes + edges through the engine without
// persisting any workflow record. Used by the SDK for what-if checks and
// by the Studio UI's "Run once" affordance. The result is a transient
// Execution echoing what an operator-driven run would have produced.
func (s *Server) SimulateWorkflow(ctx echo.Context) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	var body generated.SimulateWorkflowRequest
	if err := ctx.Bind(&body); err != nil {
		return badRequest("WORKFLOWS_BAD_REQUEST", "Invalid request body", err.Error())
	}

	trigger, err := mapping.OpenAPIToProtoTrigger(body.Trigger)
	if err != nil {
		return badRequest("WORKFLOWS_BAD_TRIGGER", "Invalid trigger payload", err.Error())
	}

	nodes := make([]*avsproto.TaskNode, 0, len(body.Nodes))
	for _, n := range body.Nodes {
		pn, err := mapping.OpenAPIToProtoNode(n)
		if err != nil {
			return badRequest("WORKFLOWS_BAD_NODE", "Invalid node payload", err.Error())
		}
		nodes = append(nodes, pn)
	}

	var edges []*avsproto.TaskEdge
	if body.Edges != nil {
		edges = make([]*avsproto.TaskEdge, 0, len(*body.Edges))
		for _, e := range *body.Edges {
			edges = append(edges, mapping.OpenAPIEdgeToProto(e))
		}
	}

	// inputVariables come in as map[string]interface{} on the wire and the
	// engine accepts that shape directly.
	inputVars := map[string]interface{}(body.InputVariables)

	chainIDs := []int64{}
	if body.ChainId != nil {
		chainIDs = append(chainIDs, *body.ChainId)
	} else if authed := restmw.UserFromContext(ctx); authed != nil && authed.ChainID != 0 {
		// Fall back to the JWT's audience chain when the caller didn't
		// pass one — same default used by RunNode for nodes:run.
		chainIDs = append(chainIDs, authed.ChainID)
	}

	exec, err := s.engine.SimulateWorkflowWithContext(ctx.Request().Context(), user, trigger, nodes, edges, inputVars, chainIDs...)
	if err != nil {
		return err
	}

	// Simulate produces a transient Execution with no persisted
	// workflow — pass an empty workflowId so the mapper still emits
	// the OpenAPI envelope (status strings, named trigger/node
	// types) instead of the raw proto enums.
	resp, mapErr := mapping.ProtoToOpenAPIExecution(exec, "")
	if mapErr != nil {
		return mapErr
	}
	return ctx.JSON(http.StatusOK, resp)
}

// EstimateWorkflowFees — POST /api/v1/workflows:estimateFees
//
// Constructs a fresh FeeEstimator per request — the dependencies
// (smart-wallet RPC, tenderly client, configured rate table, price
// service) are cheap to bundle and the estimator is stateless. SDK
// callers hit this before CreateWorkflow to surface the per-execution
// platform fee + per-node COGS + value-capture fee + any active
// discount.
func (s *Server) EstimateWorkflowFees(ctx echo.Context) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}
	var body generated.EstimateFeesRequest
	if err := ctx.Bind(&body); err != nil {
		return badRequest("FEES_BAD_REQUEST", "Invalid request body", err.Error())
	}

	// Route the fee estimator at the chain the request actually targets.
	// In gateway mode the global smartWalletRpc lands on chains[0] = mainnet
	// (see config.go's IsGateway fallback), so a Sepolia request would
	// otherwise pull mainnet gas prices and check wallet bytecode on the
	// wrong chain.
	var reqChainID int64
	if body.ChainId != nil {
		reqChainID = *body.ChainId
	}
	_, smartWalletCfg := s.resolveSmartWalletForChain(reqChainID)
	if smartWalletCfg == nil {
		return &restmw.HTTPError{
			Status: http.StatusServiceUnavailable,
			Code:   "FEES_UNAVAILABLE",
			Title:  "Fee estimator not configured",
			Detail: "This aggregator instance has no smart-wallet config for the requested chain.",
		}
	}

	// Resolve the per-chain ChainStateReader. In gateway mode this is a
	// worker-routed reader (the gateway no longer holds direct chain-RPC
	// connections for fee-estimator reads). In single-chain mode it's a
	// direct reader wrapping the aggregator's own ethclient.
	chainReader := taskengine.GetChainStateReaderForChain(uint64(reqChainID))
	if chainReader == nil {
		rpc, _ := s.resolveSmartWalletForChain(reqChainID)
		if rpc == nil {
			return &restmw.HTTPError{
				Status: http.StatusServiceUnavailable,
				Code:   "FEES_UNAVAILABLE",
				Title:  "Fee estimator not configured",
				Detail: "This aggregator instance was started without a chain-state reader for the requested chain.",
			}
		}
		chainReader = taskengine.NewDirectChainStateReader(rpc, reqChainID)
	}

	trigger, err := mapping.OpenAPIToProtoTrigger(body.Trigger)
	if err != nil {
		return badRequest("FEES_BAD_TRIGGER", "Invalid trigger payload", err.Error())
	}
	nodes := make([]*avsproto.TaskNode, 0, len(body.Nodes))
	for _, n := range body.Nodes {
		pn, err := mapping.OpenAPIToProtoNode(n)
		if err != nil {
			return badRequest("FEES_BAD_NODE", "Invalid node payload", err.Error())
		}
		nodes = append(nodes, pn)
	}
	var edges []*avsproto.TaskEdge
	if body.Edges != nil {
		edges = make([]*avsproto.TaskEdge, 0, len(*body.Edges))
		for _, e := range *body.Edges {
			edges = append(edges, mapping.OpenAPIEdgeToProto(e))
		}
	}
	inputVars, err := openAPIInputVarsOrNil(body.InputVariables)
	if err != nil {
		return badRequest("FEES_BAD_INPUT_VARS", "Invalid inputVariables payload", err.Error())
	}

	req := &avsproto.EstimateFeesReq{
		Trigger:        trigger,
		Nodes:          nodes,
		Edges:          edges,
		CreatedAt:      body.CreatedAt,
		ExpireAt:       body.ExpireAt,
		MaxExecution:   body.MaxExecution,
		InputVariables: inputVars,
	}
	if body.Runner != nil {
		req.Runner = string(*body.Runner)
	}
	if body.ChainId != nil {
		req.ChainId = *body.ChainId
	}

	var feeRatesCfg *config.FeeRatesConfig
	if s.config != nil {
		feeRatesCfg = s.config.FeeRates
	}
	estimator := taskengine.NewFeeEstimatorWithChainReader(
		s.logger, chainReader, s.engine.GetTenderlyClient(),
		smartWalletCfg, s.priceService, feeRatesCfg,
	)

	resp, err := estimator.EstimateFees(ctx.Request().Context(), req)
	if err != nil {
		s.logger.Error("estimate fees failed",
			"user", user.Address.String(),
			"error", err)
		return err
	}
	if !resp.GetSuccess() {
		return &restmw.HTTPError{
			Status: http.StatusBadRequest,
			Code:   "FEES_FAILED",
			Title:  "Fee estimation failed",
			Detail: resp.GetError(),
		}
	}
	return ctx.JSON(http.StatusOK, mapping.ProtoToOpenAPIEstimateFees(resp))
}

// openAPIInputVarsOrNil is a small wrapper around openAPIInputVarsToProto
// that returns (nil, nil) when the input pointer is unset, so handler
// code doesn't have to repeat the nil check inline.
func openAPIInputVarsOrNil(in *generated.InputVariables) (map[string]*structpb.Value, error) {
	if in == nil {
		return nil, nil
	}
	return openAPIInputVarsToProto(*in)
}

// CountWorkflows — GET /api/v1/workflows:count
//
// Returns the total number of workflows owned by the authenticated user,
// optionally narrowed by smartWalletAddress.
func (s *Server) CountWorkflows(ctx echo.Context, params generated.CountWorkflowsParams) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	req := &avsproto.GetWorkflowCountReq{}
	if params.SmartWalletAddress != nil {
		for _, addr := range *params.SmartWalletAddress {
			req.Addresses = append(req.Addresses, string(addr))
		}
	}

	resp, err := s.engine.GetWorkflowCount(user, req)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, generated.WorkflowCount{Total: resp.Total})
}

// ---------------------------------------------------------------------
// Shared helpers — kept inside the workflows handler because they are
// used here first; promote to a separate file if other resource families
// need them.
// ---------------------------------------------------------------------

// badRequest is the canonical 400 problem+json shape for handler-level
// input validation. The middleware converts the returned error into a
// problem+json response.
func badRequest(code, title, detail string) error {
	return &restmw.HTTPError{
		Status: http.StatusBadRequest,
		Code:   code,
		Title:  title,
		Detail: detail,
	}
}

// notFoundOrError lets the engine signal "not found" through whatever
// error it has on hand without each handler hand-classifying. The engine
// currently surfaces gRPC status errors; until they're translated to a
// shared error vocabulary in a follow-up, callers get the engine's
// message verbatim inside the problem+json detail.
func notFoundOrError(err error) error {
	if err == nil {
		return nil
	}
	return &restmw.HTTPError{
		Status: http.StatusNotFound,
		Code:   "WORKFLOW_NOT_FOUND",
		Title:  "Workflow not found",
		Detail: err.Error(),
	}
}

// protoPageInfoToOpenAPI lifts the proto PageInfo into the OpenAPI shape.
// The proto fields are non-optional; the OpenAPI representation makes the
// cursors optional pointers because they're absent on empty pages.
func protoPageInfoToOpenAPI(in *avsproto.PageInfo) generated.PageInfo {
	out := generated.PageInfo{
		HasNextPage:     in.GetHasNextPage(),
		HasPreviousPage: in.GetHasPreviousPage(),
	}
	if c := in.GetStartCursor(); c != "" {
		out.StartCursor = &c
	}
	if c := in.GetEndCursor(); c != "" {
		out.EndCursor = &c
	}
	return out
}

// openAPITriggerTypeToProto turns the OpenAPI Trigger discriminator
// string into the proto TriggerType enum used by TriggerTaskReq /
// SimulateTaskReq.
func openAPITriggerTypeToProto(t generated.TriggerType) (avsproto.TriggerType, error) {
	switch t {
	case generated.TriggerTypeBlock:
		return avsproto.TriggerType_TRIGGER_TYPE_BLOCK, nil
	case generated.TriggerTypeCron:
		return avsproto.TriggerType_TRIGGER_TYPE_CRON, nil
	case generated.TriggerTypeFixedTime:
		return avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME, nil
	case generated.TriggerTypeEvent:
		return avsproto.TriggerType_TRIGGER_TYPE_EVENT, nil
	case generated.TriggerTypeManual:
		return avsproto.TriggerType_TRIGGER_TYPE_MANUAL, nil
	default:
		return avsproto.TriggerType_TRIGGER_TYPE_UNSPECIFIED, errBadTriggerType(t)
	}
}

func errBadTriggerType(t generated.TriggerType) error {
	return &restmw.HTTPError{
		Status: http.StatusBadRequest,
		Code:   "WORKFLOWS_BAD_TRIGGER_TYPE",
		Title:  "Unknown trigger type",
		Detail: "triggerType must be one of: block, cron, fixedTime, event, manual (got " + string(t) + ")",
	}
}

// openAPIInputVarsToProto converts the wire-form InputVariables map
// (string → free-form JSON) into the proto structpb.Value map the engine
// stores. Errors bubble up because invalid Go values (channels, funcs)
// cannot be encoded.
func openAPIInputVarsToProto(in generated.InputVariables) (map[string]*structpb.Value, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]*structpb.Value, len(in))
	for k, raw := range in {
		pv, err := structpb.NewValue(raw)
		if err != nil {
			return nil, err
		}
		out[k] = pv
	}
	return out, nil
}

// protoExecStatusToOpenAPI maps the proto ExecutionStatus enum that
// TriggerTaskResp returns to the lowercase string used on the wire.
func protoExecStatusToOpenAPI(s avsproto.ExecutionStatus) string {
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
