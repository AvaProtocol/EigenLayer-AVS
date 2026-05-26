package mapping

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// OpenAPIToProtoNode translates the OpenAPI Node union (10 variants) into
// an engine-side *avsproto.TaskNode. The per-variant Config is roundtripped
// through camelCase JSON into the matching proto config struct — both sides
// share the same shape so a hand-mapped field-by-field copy is avoided.
//
// The Loop node is special: its Runner is itself a Node and the OpenAPI
// generator does not unmarshal nested unions automatically, so we recurse.
func OpenAPIToProtoNode(in generated.Node) (*avsproto.TaskNode, error) {
	out := &avsproto.TaskNode{Id: in.Id}
	if in.Name != nil {
		out.Name = *in.Name
	}

	discriminator, err := in.Discriminator()
	if err != nil {
		return nil, fmt.Errorf("node %s: missing discriminator: %w", in.Id, err)
	}

	switch discriminator {
	case string(generated.NodeTypeEthTransfer):
		v, err := in.AsETHTransferNode()
		if err != nil {
			return nil, fmt.Errorf("node %s: decode ETHTransferNode: %w", in.Id, err)
		}
		cfg := &avsproto.ETHTransferNode_Config{}
		if err := jsonRetargetProto(v.Config, cfg); err != nil {
			return nil, fmt.Errorf("node %s: %w", in.Id, err)
		}
		out.Type = avsproto.NodeType_NODE_TYPE_ETH_TRANSFER
		out.TaskType = &avsproto.TaskNode_EthTransfer{EthTransfer: &avsproto.ETHTransferNode{Config: cfg}}

	case string(generated.NodeTypeContractWrite):
		v, err := in.AsContractWriteNode()
		if err != nil {
			return nil, fmt.Errorf("node %s: decode ContractWriteNode: %w", in.Id, err)
		}
		cfg := &avsproto.ContractWriteNode_Config{}
		if err := jsonRetargetProto(v.Config, cfg); err != nil {
			return nil, fmt.Errorf("node %s: %w", in.Id, err)
		}
		out.Type = avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE
		out.TaskType = &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{Config: cfg}}

	case string(generated.NodeTypeContractRead):
		v, err := in.AsContractReadNode()
		if err != nil {
			return nil, fmt.Errorf("node %s: decode ContractReadNode: %w", in.Id, err)
		}
		cfg := &avsproto.ContractReadNode_Config{}
		if err := jsonRetargetProto(v.Config, cfg); err != nil {
			return nil, fmt.Errorf("node %s: %w", in.Id, err)
		}
		out.Type = avsproto.NodeType_NODE_TYPE_CONTRACT_READ
		out.TaskType = &avsproto.TaskNode_ContractRead{ContractRead: &avsproto.ContractReadNode{Config: cfg}}

	case string(generated.NodeTypeGraphqlQuery):
		v, err := in.AsGraphQLQueryNode()
		if err != nil {
			return nil, fmt.Errorf("node %s: decode GraphQLQueryNode: %w", in.Id, err)
		}
		cfg := &avsproto.GraphQLQueryNode_Config{}
		if err := jsonRetargetProto(v.Config, cfg); err != nil {
			return nil, fmt.Errorf("node %s: %w", in.Id, err)
		}
		out.Type = avsproto.NodeType_NODE_TYPE_GRAPHQL_QUERY
		out.TaskType = &avsproto.TaskNode_GraphqlQuery{GraphqlQuery: &avsproto.GraphQLQueryNode{Config: cfg}}

	case string(generated.NodeTypeRestApi):
		v, err := in.AsRestAPINode()
		if err != nil {
			return nil, fmt.Errorf("node %s: decode RestAPINode: %w", in.Id, err)
		}
		cfg := &avsproto.RestAPINode_Config{}
		if err := jsonRetargetProto(v.Config, cfg); err != nil {
			return nil, fmt.Errorf("node %s: %w", in.Id, err)
		}
		out.Type = avsproto.NodeType_NODE_TYPE_REST_API
		out.TaskType = &avsproto.TaskNode_RestApi{RestApi: &avsproto.RestAPINode{Config: cfg}}

	case string(generated.NodeTypeBranch):
		v, err := in.AsBranchNode()
		if err != nil {
			return nil, fmt.Errorf("node %s: decode BranchNode: %w", in.Id, err)
		}
		cfg := &avsproto.BranchNode_Config{}
		if err := jsonRetargetProto(v.Config, cfg); err != nil {
			return nil, fmt.Errorf("node %s: %w", in.Id, err)
		}
		out.Type = avsproto.NodeType_NODE_TYPE_BRANCH
		out.TaskType = &avsproto.TaskNode_Branch{Branch: &avsproto.BranchNode{Config: cfg}}

	case string(generated.NodeTypeFilter):
		v, err := in.AsFilterNode()
		if err != nil {
			return nil, fmt.Errorf("node %s: decode FilterNode: %w", in.Id, err)
		}
		cfg := &avsproto.FilterNode_Config{}
		if err := jsonRetargetProto(v.Config, cfg); err != nil {
			return nil, fmt.Errorf("node %s: %w", in.Id, err)
		}
		out.Type = avsproto.NodeType_NODE_TYPE_FILTER
		out.TaskType = &avsproto.TaskNode_Filter{Filter: &avsproto.FilterNode{Config: cfg}}

	case string(generated.NodeTypeLoop):
		v, err := in.AsLoopNode()
		if err != nil {
			return nil, fmt.Errorf("node %s: decode LoopNode: %w", in.Id, err)
		}
		// LoopNode.Config.Runner is a nested Node — recurse so its
		// discriminated union survives the round-trip. The runner lives
		// on the proto LoopNode (oneof), not on LoopNode_Config.
		runner, err := OpenAPIToProtoNode(v.Config.Runner)
		if err != nil {
			return nil, fmt.Errorf("node %s: loop runner: %w", in.Id, err)
		}
		cfg := &avsproto.LoopNode_Config{}
		shallow := loopConfigWithoutRunner{
			InputVariable: v.Config.InputVariable,
			IterVar:       v.Config.IterVar,
		}
		if err := jsonRetargetProto(shallow, cfg); err != nil {
			return nil, fmt.Errorf("node %s: %w", in.Id, err)
		}
		loopNode := &avsproto.LoopNode{Config: cfg}
		if err := attachLoopRunner(loopNode, runner); err != nil {
			return nil, fmt.Errorf("node %s: %w", in.Id, err)
		}
		out.Type = avsproto.NodeType_NODE_TYPE_LOOP
		out.TaskType = &avsproto.TaskNode_Loop{Loop: loopNode}

	case string(generated.NodeTypeCustomCode):
		v, err := in.AsCustomCodeNode()
		if err != nil {
			return nil, fmt.Errorf("node %s: decode CustomCodeNode: %w", in.Id, err)
		}
		// CustomCodeNode_Config.Lang is a proto enum whose wire form
		// (LANG_JAVASCRIPT) doesn't match the OpenAPI wire form
		// (javascript), so it's mapped explicitly rather than through
		// the JSON roundtrip helper.
		cfg := &avsproto.CustomCodeNode_Config{
			Source: v.Config.Source,
			Lang:   openAPILangToProto(v.Config.Lang),
		}
		out.Type = avsproto.NodeType_NODE_TYPE_CUSTOM_CODE
		out.TaskType = &avsproto.TaskNode_CustomCode{CustomCode: &avsproto.CustomCodeNode{Config: cfg}}

	case string(generated.NodeTypeBalance):
		v, err := in.AsBalanceNode()
		if err != nil {
			return nil, fmt.Errorf("node %s: decode BalanceNode: %w", in.Id, err)
		}
		cfg := &avsproto.BalanceNode_Config{}
		if err := jsonRetargetProto(v.Config, cfg); err != nil {
			return nil, fmt.Errorf("node %s: %w", in.Id, err)
		}
		out.Type = avsproto.NodeType_NODE_TYPE_BALANCE
		out.TaskType = &avsproto.TaskNode_Balance{Balance: &avsproto.BalanceNode{Config: cfg}}

	default:
		return nil, fmt.Errorf("node %s: unknown type %q", in.Id, discriminator)
	}

	return out, nil
}

// loopConfigWithoutRunner mirrors the JSON shape of LoopNodeConfig minus
// the Runner field so the JSON roundtrip into LoopNode_Config doesn't
// trip over the nested union.
type loopConfigWithoutRunner struct {
	InputVariable string  `json:"inputVariable"`
	IterVar       *string `json:"iterVar,omitempty"`
}

// jsonRetargetProto serializes any JSON-marshalable Go value and unmarshals
// the bytes into the supplied proto message via protojson. Used by Node
// mappers where the OpenAPI Config struct and the proto Config message
// share the same camelCase JSON field shape, so a hand-mapped
// field-by-field copy is unnecessary.
func jsonRetargetProto(in interface{}, out proto.Message) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal config to JSON: %w", err)
	}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, out); err != nil {
		return fmt.Errorf("unmarshal config into proto: %w", err)
	}
	return nil
}

// ProtoToOpenAPINode inverts OpenAPIToProtoNode. Each variant's Config is
// re-encoded as JSON via protojson (camelCase) and then unmarshaled into
// the OpenAPI Config Go struct. Loop nodes recurse for the runner.
func ProtoToOpenAPINode(in *avsproto.TaskNode) (generated.Node, error) {
	out := generated.Node{Id: in.GetId()}
	if n := in.GetName(); n != "" {
		out.Name = &n
	}

	switch in.GetType() {
	case avsproto.NodeType_NODE_TYPE_ETH_TRANSFER:
		t := generated.EthTransfer
		v := generated.ETHTransferNode{Type: &t}
		v.Config = &generated.ETHTransferNodeConfig{}
		if err := protoRetargetJSON(in.GetEthTransfer().GetConfig(), v.Config); err != nil {
			return out, err
		}
		out.Type = generated.NodeTypeEthTransfer
		return out, out.FromETHTransferNode(v)

	case avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE:
		t := generated.ContractWrite
		v := generated.ContractWriteNode{Type: &t}
		v.Config = &generated.ContractWriteNodeConfig{}
		if err := protoRetargetJSON(in.GetContractWrite().GetConfig(), v.Config); err != nil {
			return out, err
		}
		out.Type = generated.NodeTypeContractWrite
		return out, out.FromContractWriteNode(v)

	case avsproto.NodeType_NODE_TYPE_CONTRACT_READ:
		t := generated.ContractRead
		v := generated.ContractReadNode{Type: &t}
		v.Config = &generated.ContractReadNodeConfig{}
		if err := protoRetargetJSON(in.GetContractRead().GetConfig(), v.Config); err != nil {
			return out, err
		}
		out.Type = generated.NodeTypeContractRead
		return out, out.FromContractReadNode(v)

	case avsproto.NodeType_NODE_TYPE_GRAPHQL_QUERY:
		t := generated.GraphqlQuery
		v := generated.GraphQLQueryNode{Type: &t}
		v.Config = &generated.GraphQLQueryNodeConfig{}
		if err := protoRetargetJSON(in.GetGraphqlQuery().GetConfig(), v.Config); err != nil {
			return out, err
		}
		out.Type = generated.NodeTypeGraphqlQuery
		return out, out.FromGraphQLQueryNode(v)

	case avsproto.NodeType_NODE_TYPE_REST_API:
		t := generated.RestApi
		v := generated.RestAPINode{Type: &t}
		v.Config = &generated.RestAPINodeConfig{}
		if err := protoRetargetJSON(in.GetRestApi().GetConfig(), v.Config); err != nil {
			return out, err
		}
		out.Type = generated.NodeTypeRestApi
		return out, out.FromRestAPINode(v)

	case avsproto.NodeType_NODE_TYPE_BRANCH:
		t := generated.Branch
		v := generated.BranchNode{Type: &t}
		v.Config = &generated.BranchNodeConfig{}
		if err := protoRetargetJSON(in.GetBranch().GetConfig(), v.Config); err != nil {
			return out, err
		}
		out.Type = generated.NodeTypeBranch
		return out, out.FromBranchNode(v)

	case avsproto.NodeType_NODE_TYPE_FILTER:
		t := generated.Filter
		v := generated.FilterNode{Type: &t}
		v.Config = &generated.FilterNodeConfig{}
		if err := protoRetargetJSON(in.GetFilter().GetConfig(), v.Config); err != nil {
			return out, err
		}
		out.Type = generated.NodeTypeFilter
		return out, out.FromFilterNode(v)

	case avsproto.NodeType_NODE_TYPE_LOOP:
		t := generated.Loop
		v := generated.LoopNode{Type: &t}
		v.Config = &generated.LoopNodeConfig{}
		if err := protoRetargetJSON(in.GetLoop().GetConfig(), v.Config); err != nil {
			return out, err
		}
		runner, err := protoLoopRunnerToOpenAPI(in.GetLoop())
		if err != nil {
			return out, err
		}
		v.Config.Runner = runner
		out.Type = generated.NodeTypeLoop
		return out, out.FromLoopNode(v)

	case avsproto.NodeType_NODE_TYPE_CUSTOM_CODE:
		t := generated.CustomCode
		cfg := in.GetCustomCode().GetConfig()
		v := generated.CustomCodeNode{
			Type: &t,
			Config: &generated.CustomCodeNodeConfig{
				Lang:   protoLangToOpenAPI(cfg.GetLang()),
				Source: cfg.GetSource(),
			},
		}
		out.Type = generated.NodeTypeCustomCode
		return out, out.FromCustomCodeNode(v)

	case avsproto.NodeType_NODE_TYPE_BALANCE:
		t := generated.BalanceNodeTypeBalance
		v := generated.BalanceNode{Type: &t}
		v.Config = &generated.BalanceNodeConfig{}
		if err := protoRetargetJSON(in.GetBalance().GetConfig(), v.Config); err != nil {
			return out, err
		}
		out.Type = generated.NodeTypeBalance
		return out, out.FromBalanceNode(v)
	}

	return out, fmt.Errorf("node %s: unsupported proto type %v", in.GetId(), in.GetType())
}

// protoRetargetJSON serializes a proto message via protojson (camelCase)
// and unmarshals it into the supplied Go struct. Inverse of
// jsonRetargetProto.
func protoRetargetJSON(in proto.Message, out interface{}) error {
	raw, err := (protojson.MarshalOptions{EmitUnpopulated: false}).Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal proto config: %w", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("unmarshal config into Go struct: %w", err)
	}
	return nil
}

// protoLoopRunnerToOpenAPI converts the proto LoopNode.Runner oneof back
// into a synthetic OpenAPI Node. The synthetic node carries no id/name —
// SDKs treat the runner as inline config rather than a graph member.
func protoLoopRunnerToOpenAPI(loop *avsproto.LoopNode) (generated.Node, error) {
	tn := &avsproto.TaskNode{}
	switch r := loop.GetRunner().(type) {
	case *avsproto.LoopNode_EthTransfer:
		tn.Type = avsproto.NodeType_NODE_TYPE_ETH_TRANSFER
		tn.TaskType = &avsproto.TaskNode_EthTransfer{EthTransfer: r.EthTransfer}
	case *avsproto.LoopNode_ContractWrite:
		tn.Type = avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE
		tn.TaskType = &avsproto.TaskNode_ContractWrite{ContractWrite: r.ContractWrite}
	case *avsproto.LoopNode_ContractRead:
		tn.Type = avsproto.NodeType_NODE_TYPE_CONTRACT_READ
		tn.TaskType = &avsproto.TaskNode_ContractRead{ContractRead: r.ContractRead}
	case *avsproto.LoopNode_GraphqlDataQuery:
		tn.Type = avsproto.NodeType_NODE_TYPE_GRAPHQL_QUERY
		tn.TaskType = &avsproto.TaskNode_GraphqlQuery{GraphqlQuery: r.GraphqlDataQuery}
	case *avsproto.LoopNode_RestApi:
		tn.Type = avsproto.NodeType_NODE_TYPE_REST_API
		tn.TaskType = &avsproto.TaskNode_RestApi{RestApi: r.RestApi}
	case *avsproto.LoopNode_CustomCode:
		tn.Type = avsproto.NodeType_NODE_TYPE_CUSTOM_CODE
		tn.TaskType = &avsproto.TaskNode_CustomCode{CustomCode: r.CustomCode}
	case nil:
		return generated.Node{}, fmt.Errorf("loop: runner is nil")
	default:
		return generated.Node{}, fmt.Errorf("loop: unknown runner variant %T", r)
	}
	return ProtoToOpenAPINode(tn)
}

// attachLoopRunner sets the appropriate runner oneof field on the proto
// LoopNode based on the type of the mapped runner TaskNode. The runner
// oneof is keyed on the inner node's variant rather than carried through
// TaskNode.TaskType because the loop's inner node is referenced by config
// not by edge.
func attachLoopRunner(loop *avsproto.LoopNode, runner *avsproto.TaskNode) error {
	switch r := runner.TaskType.(type) {
	case *avsproto.TaskNode_EthTransfer:
		loop.Runner = &avsproto.LoopNode_EthTransfer{EthTransfer: r.EthTransfer}
	case *avsproto.TaskNode_ContractWrite:
		loop.Runner = &avsproto.LoopNode_ContractWrite{ContractWrite: r.ContractWrite}
	case *avsproto.TaskNode_ContractRead:
		loop.Runner = &avsproto.LoopNode_ContractRead{ContractRead: r.ContractRead}
	case *avsproto.TaskNode_GraphqlQuery:
		loop.Runner = &avsproto.LoopNode_GraphqlDataQuery{GraphqlDataQuery: r.GraphqlQuery}
	case *avsproto.TaskNode_RestApi:
		loop.Runner = &avsproto.LoopNode_RestApi{RestApi: r.RestApi}
	case *avsproto.TaskNode_CustomCode:
		loop.Runner = &avsproto.LoopNode_CustomCode{CustomCode: r.CustomCode}
	default:
		return fmt.Errorf("loop: runner node type %T is not supported", runner.TaskType)
	}
	return nil
}
