package rest

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/mapping"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/labstack/echo/v4"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PrepareWorkflow reuses the existing workflow definition and graph runner;
// only the terminal write enters the separate owner-signing handoff.
func (s *Server) PrepareWorkflow(ctx echo.Context) error {
	p, err := s.ensurePermission(ctx, OpPrepareWorkflow)
	if err != nil {
		return err
	}
	var body generated.PrepareWorkflowRequest
	if err := decodeOwnerExecutionBody(ctx, &body); err != nil {
		return err
	}
	trigger, err := mapping.OpenAPIToProtoTrigger(body.Trigger)
	if err != nil {
		return badRequest("OWNER_EXECUTION_INVALID", "Invalid owner workflow trigger", err.Error())
	}
	nodes := make([]*avsproto.TaskNode, len(body.Nodes))
	for i, node := range body.Nodes {
		nodes[i], err = mapping.OpenAPIToProtoNode(node)
		if err != nil {
			return badRequest("OWNER_EXECUTION_INVALID", "Invalid owner workflow node", err.Error())
		}
	}
	var edges []*avsproto.TaskEdge
	if body.Edges != nil {
		for _, edge := range *body.Edges {
			edges = append(edges, mapping.OpenAPIEdgeToProto(edge))
		}
	}
	prepared, err := s.engine.PrepareOwnerWorkflow(ctx.Request().Context(), p.User, trigger, nodes, edges, map[string]interface{}(body.InputVariables), body.ChainId)
	if err != nil {
		return mapOwnerExecutionError(err)
	}
	execution, err := mapping.ProtoToOpenAPIExecution(prepared.Execution, "")
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(prepared.Operation)
	if err != nil {
		return err
	}
	var operation generated.OwnerUserOperationV07
	if err := json.Unmarshal(encoded, &operation); err != nil {
		return err
	}
	calls := make([]generated.UserOpInnerCall, len(prepared.Calls))
	for i, call := range prepared.Calls {
		calls[i] = generated.UserOpInnerCall{To: generated.EthereumAddress(call.To), Value: call.Value, Selector: call.Selector, Data: generated.Hex(call.Data)}
	}
	funding := generated.PrepareWorkflowResponseGasFunding("source")
	if prepared.Operation.Paymaster != nil {
		funding = generated.PrepareWorkflowResponseGasFunding("sponsored")
	}
	return ctx.JSON(http.StatusOK, generated.PrepareWorkflowResponse{
		Execution: execution, UserOperation: operation,
		UserOpHash: generated.Bytes32(prepared.UserOpHash), Owner: generated.EthereumAddress(p.User.Address.Hex()),
		ChainId: prepared.ChainID, EntryPoint: generated.EthereumAddress(preset.EntryPointV07().Hex()),
		SigningMethod: generated.PrepareWorkflowResponseSigningMethod("personal_sign"),
		ExpiresAt:     prepared.ExpiresAt, WriteNodeId: prepared.WriteNodeID,
		Calls: calls, GasFunding: funding, MaxGasCostWei: preset.MaxOwnerGasCost(prepared.Operation).String(),
	})
}

// SubmitExecution receives no operation fields, only the owner's signature.
func (s *Server) SubmitExecution(ctx echo.Context, id generated.Ulid) error {
	p, err := s.ensurePermission(ctx, OpSubmitExecution)
	if err != nil {
		return err
	}
	var body generated.SubmitExecutionRequest
	if err := decodeOwnerExecutionBody(ctx, &body); err != nil {
		return err
	}
	signature, err := hexutil.Decode(body.Signature)
	if err != nil || len(signature) != 65 {
		return badRequest("OWNER_EXECUTION_INVALID", "Invalid owner signature", "signature must be a 65-byte hex personal signature")
	}
	submitted, err := s.engine.SubmitOwnerExecution(ctx.Request().Context(), p.User, string(id), signature)
	if err != nil {
		return mapOwnerExecutionError(err)
	}
	return ctx.JSON(http.StatusAccepted, submitted)
}

func decodeOwnerExecutionBody(ctx echo.Context, body interface{}) error {
	decoder := json.NewDecoder(http.MaxBytesReader(ctx.Response(), ctx.Request().Body, 256*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(body); err != nil {
		return badRequest("OWNER_EXECUTION_INVALID", "Invalid owner execution request", err.Error())
	}
	if err := decoder.Decode(new(interface{})); err != io.EOF {
		return badRequest("OWNER_EXECUTION_INVALID", "Invalid owner execution request", "body must contain exactly one JSON object")
	}
	return nil
}

func mapOwnerExecutionError(err error) error {
	st, ok := status.FromError(err)
	if ok {
		switch st.Code() {
		case codes.InvalidArgument:
			return badRequest("OWNER_EXECUTION_INVALID", "Invalid owner execution request", st.Message())
		case codes.PermissionDenied:
			return &restmw.HTTPError{Status: http.StatusForbidden, Code: "OWNER_EXECUTION_SOURCE_FORBIDDEN", Title: "Source wallet is not supported for this owner", Detail: st.Message()}
		case codes.NotFound:
			return &restmw.HTTPError{Status: http.StatusNotFound, Code: "OWNER_EXECUTION_NOT_FOUND", Title: "Prepared execution not found", Detail: st.Message()}
		case codes.FailedPrecondition:
			return &restmw.HTTPError{Status: http.StatusPreconditionFailed, Code: "OWNER_EXECUTION_PREPARE_REQUIRED", Title: "Prepare and review again", Detail: st.Message()}
		case codes.ResourceExhausted:
			return &restmw.HTTPError{Status: http.StatusTooManyRequests, Code: "OWNER_EXECUTION_LIMIT", Title: "Too many owner executions", Detail: st.Message()}
		}
	}
	return &restmw.HTTPError{Status: http.StatusBadGateway, Code: "OWNER_EXECUTION_FAILED", Title: "Owner execution unavailable"}
}
