package rest

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Workflows resource — see api/openapi.yaml `tags: [Workflows]`.
//
// Every method delegates to taskengine.Engine after parsing the request
// and (eventually) translating the OpenAPI-generated types to the engine's
// internal protobuf types. During the engine rename (Task -> Workflow),
// this whole file becomes the natural integration point — handlers stay
// stubbed here until the rename + handler bodies land together.

// CreateWorkflow — POST /api/v1/workflows
func (s *Server) CreateWorkflow(ctx echo.Context) error {
	return s.notImplemented(ctx, "workflows.create")
}

// ListWorkflows — GET /api/v1/workflows
func (s *Server) ListWorkflows(ctx echo.Context, params generated.ListWorkflowsParams) error {
	return s.notImplemented(ctx, "workflows.list")
}

// GetWorkflow — GET /api/v1/workflows/{id}
func (s *Server) GetWorkflow(ctx echo.Context, id generated.Ulid) error {
	return s.notImplemented(ctx, "workflows.retrieve")
}

// CancelWorkflow — DELETE /api/v1/workflows/{id}
func (s *Server) CancelWorkflow(ctx echo.Context, id generated.Ulid) error {
	return s.notImplemented(ctx, "workflows.cancel")
}

// PauseWorkflow — POST /api/v1/workflows/{id}:pause
func (s *Server) PauseWorkflow(ctx echo.Context, id generated.Ulid) error {
	return s.notImplemented(ctx, "workflows.pause")
}

// ResumeWorkflow — POST /api/v1/workflows/{id}:resume
func (s *Server) ResumeWorkflow(ctx echo.Context, id generated.Ulid) error {
	return s.notImplemented(ctx, "workflows.resume")
}

// TriggerWorkflow — POST /api/v1/workflows/{id}:trigger
func (s *Server) TriggerWorkflow(ctx echo.Context, id generated.Ulid) error {
	return s.notImplemented(ctx, "workflows.trigger")
}

// SimulateWorkflow — POST /api/v1/workflows:simulate
func (s *Server) SimulateWorkflow(ctx echo.Context) error {
	return s.notImplemented(ctx, "workflows.simulate")
}

// EstimateWorkflowFees — POST /api/v1/workflows:estimateFees
func (s *Server) EstimateWorkflowFees(ctx echo.Context) error {
	return s.notImplemented(ctx, "workflows.estimateFees")
}

// CountWorkflows — GET /api/v1/workflows:count
//
// Returns the total number of workflows owned by the authenticated user.
// Currently only `smartWalletAddress` filters are pushed down; `status`
// and `chainId` filters narrow the count after the engine returns.
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
