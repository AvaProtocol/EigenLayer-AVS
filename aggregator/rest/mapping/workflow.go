package mapping

import (
	"fmt"

	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// OpenAPIToProtoCreateWorkflow turns the OpenAPI CreateWorkflowRequest into
// the proto CreateTaskReq the engine accepts. Used by POST /workflows.
//
// Fields that the engine derives server-side (Id, Owner, CreatedAt,
// CompletedAt, ExecutionCount) are deliberately not set here.
func OpenAPIToProtoCreateWorkflow(in generated.CreateWorkflowRequest) (*avsproto.CreateTaskReq, error) {
	trigger, err := OpenAPIToProtoTrigger(in.Trigger)
	if err != nil {
		return nil, err
	}

	nodes := make([]*avsproto.TaskNode, 0, len(in.Nodes))
	for _, n := range in.Nodes {
		pn, err := OpenAPIToProtoNode(n)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, pn)
	}

	var edges []*avsproto.TaskEdge
	if in.Edges != nil {
		edges = make([]*avsproto.TaskEdge, 0, len(*in.Edges))
		for _, e := range *in.Edges {
			edges = append(edges, OpenAPIEdgeToProto(e))
		}
	}

	vars, err := openAPIInputVariablesToProto(in.InputVariables)
	if err != nil {
		return nil, err
	}

	out := &avsproto.CreateTaskReq{
		Trigger:        trigger,
		Nodes:          nodes,
		Edges:          edges,
		InputVariables: vars,
	}
	if in.StartAt != nil {
		out.StartAt = *in.StartAt
	}
	if in.ExpiredAt != nil {
		out.ExpiredAt = *in.ExpiredAt
	}
	if in.MaxExecution != nil {
		out.MaxExecution = *in.MaxExecution
	}
	if in.ChainId != nil {
		out.ChainId = *in.ChainId
	}
	// Note: in.Name and in.SmartWalletAddress flow through
	// inputVariables.settings (name + runner) per the existing
	// engine contract — they are NOT direct fields on CreateTaskReq.

	return out, nil
}

// ProtoToOpenAPIWorkflow turns a stored *avsproto.Task into the OpenAPI
// Workflow envelope. Used by GET /workflows/{id}, ListWorkflows, and
// CreateWorkflow's response body.
func ProtoToOpenAPIWorkflow(in *avsproto.Task) (generated.Workflow, error) {
	out := generated.Workflow{
		Id:                 generated.Ulid(in.GetId()),
		Owner:              generated.EthereumAddress(in.GetOwner()),
		SmartWalletAddress: generated.EthereumAddress(in.GetSmartWalletAddress()),
		Status:             generated.WorkflowStatus(protoTaskStatusToOpenAPI(in.GetStatus())),
	}
	if n := in.GetName(); n != "" {
		out.Name = &n
	}
	if v := in.GetStartAt(); v != 0 {
		out.StartAt = &v
	}
	if v := in.GetExpiredAt(); v != 0 {
		out.ExpiredAt = &v
	}
	if v := in.GetCompletedAt(); v != 0 {
		out.CompletedAt = &v
	}
	if v := in.GetMaxExecution(); v != 0 {
		out.MaxExecution = &v
	}
	if v := in.GetExecutionCount(); v != 0 {
		out.ExecutionCount = &v
	}
	if v := in.GetChainId(); v != 0 {
		out.ChainId = &v
	}

	trig, err := ProtoToOpenAPITrigger(in.GetTrigger())
	if err != nil {
		return out, err
	}
	out.Trigger = trig

	out.Nodes = make([]generated.Node, 0, len(in.GetNodes()))
	for _, n := range in.GetNodes() {
		node, err := ProtoToOpenAPINode(n)
		if err != nil {
			return out, err
		}
		out.Nodes = append(out.Nodes, node)
	}

	if edges := in.GetEdges(); len(edges) > 0 {
		mapped := make([]generated.Edge, 0, len(edges))
		for _, e := range edges {
			mapped = append(mapped, ProtoEdgeToOpenAPI(e))
		}
		out.Edges = &mapped
	}

	if iv := in.GetInputVariables(); len(iv) > 0 {
		vars := protoInputVariablesToOpenAPI(iv)
		out.InputVariables = &vars
	}

	return out, nil
}

// OpenAPIEdgeToProto is a trivial field copy — kept exported so SDK-facing
// helpers can call it without recreating the edge inline.
func OpenAPIEdgeToProto(e generated.Edge) *avsproto.TaskEdge {
	return &avsproto.TaskEdge{Id: e.Id, Source: e.Source, Target: e.Target}
}

// ProtoEdgeToOpenAPI is the inverse of OpenAPIEdgeToProto.
func ProtoEdgeToOpenAPI(e *avsproto.TaskEdge) generated.Edge {
	return generated.Edge{Id: e.GetId(), Source: e.GetSource(), Target: e.GetTarget()}
}

// openAPIInputVariablesToProto translates the free-form input variables
// map into the proto map<string, structpb.Value> shape. Errors bubble up
// because invalid JSON-like values (channels, funcs) cannot be encoded.
func openAPIInputVariablesToProto(in *generated.InputVariables) (map[string]*structpb.Value, error) {
	if in == nil || len(*in) == 0 {
		return nil, nil
	}
	out := make(map[string]*structpb.Value, len(*in))
	for k, raw := range *in {
		pv, err := structpb.NewValue(raw)
		if err != nil {
			return nil, fmt.Errorf("inputVariables[%s]: %w", k, err)
		}
		out[k] = pv
	}
	return out, nil
}

// protoInputVariablesToOpenAPI inverts openAPIInputVariablesToProto.
func protoInputVariablesToOpenAPI(in map[string]*structpb.Value) generated.InputVariables {
	out := make(generated.InputVariables, len(in))
	for k, v := range in {
		out[k] = v.AsInterface()
	}
	return out
}

// protoTaskStatusToOpenAPI maps the proto TaskStatus enum to the OpenAPI
// string vocabulary used by the WorkflowStatus discriminator. The proto
// names are SCREAMING_SNAKE_CASE; the wire form is lowercase.
func protoTaskStatusToOpenAPI(s avsproto.TaskStatus) string {
	switch s {
	case avsproto.TaskStatus_Enabled:
		return "enabled"
	case avsproto.TaskStatus_Disabled:
		return "disabled"
	case avsproto.TaskStatus_Running:
		return "running"
	case avsproto.TaskStatus_Completed:
		return "completed"
	case avsproto.TaskStatus_Failed:
		return "failed"
	default:
		return "enabled"
	}
}
