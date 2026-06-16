package rest

import (
	"encoding/json"
	"net/http"

	"github.com/labstack/echo/v4"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/mapping"
	restmw "github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/middleware"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Nodes resource — see api/openapi.yaml `tags: [Nodes]`.

// RunNode — POST /api/v1/nodes:run
//
// Execute a single node definition against inline input variables
// without persisting a workflow. Used by SDK testing flows and the
// agent-CLI verify command.
func (s *Server) RunNode(ctx echo.Context) error {
	user, err := s.requireUser(ctx)
	if err != nil {
		return err
	}

	var body generated.RunNodeRequest
	if err := ctx.Bind(&body); err != nil {
		return badRequest("NODES_BAD_REQUEST", "Invalid request body", err.Error())
	}

	node, err := mapping.OpenAPIToProtoNode(body.Node)
	if err != nil {
		return badRequest("NODES_BAD_NODE", "Invalid node payload", err.Error())
	}

	req := &avsproto.RunNodeWithInputsReq{Node: node}
	if body.ChainId != nil {
		req.ChainId = *body.ChainId
	} else if authed := restmw.UserFromContext(ctx); authed != nil && authed.ChainID != 0 {
		// Default to the JWT's audience chain when the caller didn't pass
		// one explicitly. The audience is set at mint time to the chain
		// the smart wallet lives on, which is the right default for the
		// in-process node executor (and the existing
		// extractSettingsChainID fallback in RunNodeImmediately).
		req.ChainId = authed.ChainID
	}
	if body.InputVariables != nil {
		converted, err := openAPIInputVarsToProto(*body.InputVariables)
		if err != nil {
			return badRequest("NODES_BAD_INPUT_VARS", "Invalid inputVariables payload", err.Error())
		}
		req.InputVariables = converted
	}

	resp, err := s.engine.RunNodeImmediatelyRPCWithContext(ctx.Request().Context(), user, req)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, runNodeRespToOpenAPI(resp))
}

// runNodeRespToOpenAPI maps the engine RunNodeWithInputsResp to the
// OpenAPI RunNodeResponse. The per-type OutputData oneof is flattened
// to a generic map[string]interface{} via protojson — the SDK consumes
// the variant payload as a map keyed on the node type.
func runNodeRespToOpenAPI(in *avsproto.RunNodeWithInputsResp) generated.RunNodeResponse {
	out := generated.RunNodeResponse{Success: in.GetSuccess()}
	if msg := in.GetError(); msg != "" {
		out.Error = &msg
	}
	if code := in.GetErrorCode().String(); code != "" && code != "ERROR_CODE_UNSPECIFIED" {
		out.ErrorCode = &code
	}
	if md := in.GetMetadata(); md != nil {
		if v, ok := md.AsInterface().(map[string]interface{}); ok && v != nil {
			out.Metadata = &v
		}
	}
	if ec := in.GetExecutionContext(); ec != nil {
		if v, ok := ec.AsInterface().(map[string]interface{}); ok && v != nil {
			out.ExecutionContext = &v
		}
	}
	// The proto's OutputData oneof labels each variant with its own
	// JSON key (ethTransfer, restApi, etc.) under protojson. We surface
	// the first variant key as the canonical `output` map; SDK callers
	// pick the field they expect based on the node type they sent.
	if raw, err := (protojson.MarshalOptions{}).Marshal(in); err == nil {
		var envelope map[string]interface{}
		if json.Unmarshal(raw, &envelope) == nil {
			for _, key := range []string{"ethTransfer", "graphql", "contractRead", "contractWrite", "customCode", "restApi", "branch", "filter", "loop", "balance"} {
				if v, ok := envelope[key].(map[string]interface{}); ok {
					out.Output = &v
					break
				}
			}
		}
	}
	return out
}
