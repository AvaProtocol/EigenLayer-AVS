package rest

import (
	"bytes"
	"encoding/json"
	"io"
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
	// No-fund operation: a user JWT or a partner assertion both authorize it.
	user, err := s.requireSimulateAuth(ctx)
	if err != nil {
		return err
	}

	// AA23 debug: capture the raw HTTP body BEFORE Bind consumes it, so we can
	// prove whether Studio's JSON carried methodCalls[].contractAddress on the wire.
	// Grep gateway.log for "AA23_DEBUG". Temporary — remove once root cause is closed.
	// Always restore Body so Bind still works even when ReadAll fails mid-stream.
	rawBody, rawErr := io.ReadAll(ctx.Request().Body)
	ctx.Request().Body = io.NopCloser(bytes.NewReader(rawBody))
	if rawErr != nil {
		if s.logger != nil {
			s.logger.Warn("AA23_DEBUG RunNode: failed to read raw body", "error", rawErr.Error(), "bytes_read", len(rawBody))
		}
	} else {
		s.logAA23RawBodyMethodCalls(rawBody)
	}

	var body generated.RunNodeRequest
	if err := ctx.Bind(&body); err != nil {
		return badRequest("NODES_BAD_REQUEST", "Invalid request body", err.Error())
	}

	s.logAA23OpenAPIMethodCalls("post-bind", body.Node)

	node, err := mapping.OpenAPIToProtoNode(body.Node)
	if err != nil {
		return badRequest("NODES_BAD_NODE", "Invalid node payload", err.Error())
	}

	s.logAA23ProtoMethodCalls("post-map", node)

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
	if body.Erc20Overrides != nil {
		req.Erc20Overrides = openAPIERC20OverridesToProto(*body.Erc20Overrides)
	}

	// A caller-supplied Idempotency-Key (Stripe-style header) makes a retried or
	// double-submitted execute safe: the same key returns the first result instead
	// of broadcasting a second UserOp. Absent the header, behavior is unchanged.
	idempotencyKey := ctx.Request().Header.Get("Idempotency-Key")
	resp, err := s.engine.RunNodeImmediatelyRPCIdempotent(ctx.Request().Context(), user, req, idempotencyKey)
	if err != nil {
		return err
	}
	return ctx.JSON(http.StatusOK, runNodeRespToOpenAPI(resp))
}

// logAA23RawBodyMethodCalls extracts methodCalls[].contractAddress from the raw
// HTTP JSON body before any struct Bind. Proves what Studio put on the wire.
// Temporary AA23 instrumentation — remove when closed.
func (s *Server) logAA23RawBodyMethodCalls(raw []byte) {
	if s.logger == nil {
		return
	}
	var probe struct {
		Node *struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Config *struct {
				ContractAddress string `json:"contractAddress"`
				ChainID         int64  `json:"chainId"`
				IsSimulated     *bool  `json:"isSimulated"`
				MethodCalls     []struct {
					MethodName      string   `json:"methodName"`
					ContractAddress *string  `json:"contractAddress"`
					MethodParams    []string `json:"methodParams"`
				} `json:"methodCalls"`
			} `json:"config"`
		} `json:"node"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		s.logger.Warn("AA23_DEBUG RunNode.raw-body: JSON parse failed",
			"error", err.Error(), "body_bytes", len(raw))
		return
	}
	if probe.Node == nil || probe.Node.Config == nil {
		s.logger.Debug("AA23_DEBUG RunNode.raw-body: no contractWrite config",
			"body_bytes", len(raw))
		return
	}
	cfg := probe.Node.Config
	type callSnap struct {
		Index              int    `json:"index"`
		MethodName         string `json:"methodName"`
		HasContractAddress bool   `json:"hasContractAddress"`
		ContractAddress    string `json:"contractAddress"`
		MethodParamsCount  int    `json:"methodParamsCount"`
	}
	calls := make([]callSnap, 0, len(cfg.MethodCalls))
	for i, mc := range cfg.MethodCalls {
		snap := callSnap{
			Index:             i,
			MethodName:        mc.MethodName,
			MethodParamsCount: len(mc.MethodParams),
		}
		if mc.ContractAddress != nil {
			snap.HasContractAddress = true
			snap.ContractAddress = *mc.ContractAddress
		}
		calls = append(calls, snap)
	}
	isSim := interface{}(nil)
	if cfg.IsSimulated != nil {
		isSim = *cfg.IsSimulated
	}
	s.logger.Debug("AA23_DEBUG RunNode.raw-body",
		"body_bytes", len(raw),
		"node_id", probe.Node.ID,
		"node_type", probe.Node.Type,
		"node_contract", cfg.ContractAddress,
		"chain_id", cfg.ChainID,
		"is_simulated", isSim,
		"method_call_count", len(calls),
		"method_calls", calls,
	)
}

// logAA23OpenAPIMethodCalls logs methodCalls after Echo Bind + AsContractWriteNode.
// Temporary AA23 instrumentation.
func (s *Server) logAA23OpenAPIMethodCalls(stage string, n generated.Node) {
	if s.logger == nil {
		return
	}
	if n.Type != generated.NodeTypeContractWrite {
		s.logger.Debug("AA23_DEBUG RunNode."+stage+": not contractWrite",
			"node_id", n.Id, "node_type", string(n.Type))
		return
	}
	cw, err := n.AsContractWriteNode()
	if err != nil || cw.Config == nil {
		s.logger.Warn("AA23_DEBUG RunNode."+stage+": AsContractWriteNode failed",
			"node_id", n.Id, "error", errString(err))
		return
	}
	cfg := cw.Config
	type callSnap struct {
		Index              int    `json:"index"`
		MethodName         string `json:"methodName"`
		HasContractAddress bool   `json:"hasContractAddress"`
		ContractAddress    string `json:"contractAddress"`
	}
	var calls []callSnap
	if cfg.MethodCalls != nil {
		for i, mc := range *cfg.MethodCalls {
			snap := callSnap{Index: i, MethodName: mc.MethodName}
			if mc.ContractAddress != nil {
				snap.HasContractAddress = true
				snap.ContractAddress = string(*mc.ContractAddress)
			}
			calls = append(calls, snap)
		}
	}
	s.logger.Debug("AA23_DEBUG RunNode."+stage,
		"node_id", n.Id,
		"node_contract", string(cfg.ContractAddress),
		"chain_id", cfg.ChainId,
		"method_call_count", len(calls),
		"method_calls", calls,
	)
}

// logAA23ProtoMethodCalls logs methodCalls after OpenAPI→proto mapping.
// Temporary AA23 instrumentation.
func (s *Server) logAA23ProtoMethodCalls(stage string, node *avsproto.TaskNode) {
	if s.logger == nil || node == nil {
		return
	}
	cw := node.GetContractWrite()
	if cw == nil || cw.GetConfig() == nil {
		s.logger.Debug("AA23_DEBUG RunNode."+stage+": no ContractWrite config",
			"node_id", node.GetId(), "type", node.GetType().String())
		return
	}
	cfg := cw.GetConfig()
	type callSnap struct {
		Index              int    `json:"index"`
		MethodName         string `json:"methodName"`
		HasContractAddress bool   `json:"hasContractAddress"`
		ContractAddress    string `json:"contractAddress"`
	}
	calls := make([]callSnap, 0, len(cfg.GetMethodCalls()))
	for i, mc := range cfg.GetMethodCalls() {
		addr := mc.GetContractAddress()
		calls = append(calls, callSnap{
			Index:              i,
			MethodName:         mc.GetMethodName(),
			HasContractAddress: mc.ContractAddress != nil && addr != "",
			ContractAddress:    addr,
		})
	}
	s.logger.Debug("AA23_DEBUG RunNode."+stage,
		"node_id", node.GetId(),
		"node_contract", cfg.GetContractAddress(),
		"chain_id", cfg.GetChainId(),
		"method_call_count", len(calls),
		"method_calls", calls,
	)
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// openAPIERC20OverridesToProto maps the REST ERC20StateOverride list onto the
// proto representation consumed by RunNodeImmediately. Slot indices are widened
// from the spec's int64 to the proto's uint64. A negative slot is invalid (a
// storage slot is a non-negative index), so it is treated as unset rather than
// silently coerced to slot 0 — and because the engine requires an explicit slot
// whenever the corresponding balance/allowance is set, a missing or negative
// slot surfaces as a clear validation error instead of a wrong-slot seed. The
// engine validates addresses/values.
func openAPIERC20OverridesToProto(in []generated.ERC20StateOverride) []*avsproto.ERC20StateOverride {
	if len(in) == 0 {
		return nil
	}
	out := make([]*avsproto.ERC20StateOverride, 0, len(in))
	for _, o := range in {
		po := &avsproto.ERC20StateOverride{
			TokenAddress: string(o.TokenAddress),
			OwnerAddress: string(o.OwnerAddress),
			Balance:      o.Balance,
			Allowance:    o.Allowance,
		}
		if o.SpenderAddress != nil {
			s := string(*o.SpenderAddress)
			po.SpenderAddress = &s
		}
		po.BalanceSlot = nonNegativeSlotPtr(o.BalanceSlot)
		po.AllowanceSlot = nonNegativeSlotPtr(o.AllowanceSlot)
		out = append(out, po)
	}
	return out
}

// nonNegativeSlotPtr widens a spec int64 storage slot to the proto's uint64,
// returning nil when the slot is absent or negative. A nil slot is then
// rejected by the engine (an explicit slot is required when balance/allowance
// is set), so a negative value surfaces as a validation error rather than a
// silent slot-0 seed.
func nonNegativeSlotPtr(v *int64) *uint64 {
	if v == nil || *v < 0 {
		return nil
	}
	u := uint64(*v)
	return &u
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
		switch v := md.AsInterface().(type) {
		case map[string]interface{}:
			if v != nil {
				out.Metadata = &v
			}
		case []interface{}:
			// Some node types (notably contractWrite) produce a per-method
			// results array as metadata. RunNodeResponse.metadata is an object,
			// so a bare array was previously dropped here — taking the per-method
			// receipts (executionStatus, userOpHash, transactionHash) with it.
			// Wrap it under "results" so those reach the client — including an
			// EMPTY array (results: []), so callers always see a consistent shape
			// rather than metadata=null for a legitimately empty result set.
			if v == nil {
				v = []interface{}{}
			}
			wrapped := map[string]interface{}{"results": v}
			out.Metadata = &wrapped
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
