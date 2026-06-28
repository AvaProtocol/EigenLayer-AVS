package mapping

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// ProtoToOpenAPIExecution lifts an engine *avsproto.Execution into the
// OpenAPI Execution envelope. The workflowId comes from outside the
// proto Execution because executions are stored under their workflow's
// chain-scoped key prefix — there's no Execution.WorkflowId on the
// proto, so the caller (a handler that already looked the workflow up)
// passes it explicitly.
//
// Steps and fee fields round-trip via protojson because the camelCase
// shapes match field-for-field; ExecutionStatus is normalized to the
// lowercase wire vocabulary.
func ProtoToOpenAPIExecution(in *avsproto.Execution, workflowID string) (generated.Execution, error) {
	out := generated.Execution{
		Id:         generated.Ulid(in.GetId()),
		WorkflowId: generated.Ulid(workflowID),
		StartAt:    in.GetStartAt(),
		Status:     generated.ExecutionStatus(executionStatusProtoToWire(in.GetStatus())),
	}
	if v := in.GetEndAt(); v != 0 {
		out.EndAt = &v
	}
	// Always include the index — the first execution legitimately
	// has index=0, and an `omitempty` int wouldn't survive the JSON
	// marshal even if assigned. We use a pointer so omitempty only
	// drops it when explicitly nil.
	{
		v := in.GetIndex()
		out.Index = &v
	}
	if msg := in.GetError(); msg != "" {
		out.Error = &msg
	}

	if steps := in.GetSteps(); len(steps) > 0 {
		mapped := make([]generated.ExecutionStep, 0, len(steps))
		for _, s := range steps {
			step, err := protoExecutionStepToOpenAPI(s)
			if err != nil {
				return out, fmt.Errorf("step %s: %w", s.GetId(), err)
			}
			mapped = append(mapped, step)
		}
		out.Steps = &mapped
	}

	if cogs := in.GetCogs(); len(cogs) > 0 {
		mapped := make([]generated.NodeCOGS, 0, len(cogs))
		for _, c := range cogs {
			var node generated.NodeCOGS
			if err := protoRetargetJSON(c, &node); err != nil {
				return out, fmt.Errorf("cogs: %w", err)
			}
			mapped = append(mapped, node)
		}
		out.Cogs = &mapped
	}

	if fee := in.GetExecutionFee(); fee != nil {
		var f generated.Fee
		if err := protoRetargetJSON(fee, &f); err != nil {
			return out, fmt.Errorf("executionFee: %w", err)
		}
		out.ExecutionFee = &f
	}

	if vf := in.GetValueFee(); vf != nil {
		var f generated.ValueFee
		if err := protoRetargetJSON(vf, &f); err != nil {
			return out, fmt.Errorf("valueFee: %w", err)
		}
		out.ValueFee = &f
	}

	return out, nil
}

// ProtoExecutionToOpenAPISummary lifts an Execution into the lightweight
// status summary returned by the GetExecutionStatus and StreamExecution
// endpoints. Skips the steps/cogs/fee payload to keep responses small.
func ProtoExecutionToOpenAPISummary(in *avsproto.Execution, workflowID string) generated.ExecutionStatusSummary {
	out := generated.ExecutionStatusSummary{
		Id:     generated.Ulid(in.GetId()),
		Status: generated.ExecutionStatus(executionStatusProtoToWire(in.GetStatus())),
	}
	if v := in.GetStartAt(); v != 0 {
		out.StartAt = &v
	}
	if v := in.GetEndAt(); v != 0 {
		out.EndAt = &v
	}
	if msg := in.GetError(); msg != "" {
		out.Error = &msg
	}
	if workflowID != "" {
		wid := generated.Ulid(workflowID)
		out.WorkflowId = &wid
	}
	return out
}

// protoExecutionStepToOpenAPI converts a single step. The step's per-type
// Output oneof is flattened into the OpenAPI `output` map by marshalling
// the proto via protojson and pulling the first known variant field
// (mirrors how runNodeRespToOpenAPI handles RunNodeWithInputsResp).
//
// Scalars are hand-mapped rather than roundtripped through JSON because
// protojson emits int64 fields as JSON strings (per the protobuf JSON
// spec), which can't unmarshal into the generated `int64` slots on
// ExecutionStep. Going field-by-field is also typesafe.
func protoExecutionStepToOpenAPI(in *avsproto.Execution_Step) (generated.ExecutionStep, error) {
	out := generated.ExecutionStep{
		Id:      in.GetId(),
		Type:    normalizeStepType(in.GetType()),
		Success: in.GetSuccess(),
	}
	if v := in.GetName(); v != "" {
		out.Name = &v
	}
	if v := in.GetError(); v != "" {
		out.Error = &v
	}
	if code := in.GetErrorCode().String(); code != "" && code != "ERROR_CODE_UNSPECIFIED" {
		out.ErrorCode = &code
	}
	if v := in.GetLog(); v != "" {
		out.Log = &v
	}
	if v := in.GetStartAt(); v != 0 {
		out.StartAt = &v
	}
	if v := in.GetEndAt(); v != 0 {
		out.EndAt = &v
	}
	if v := in.GetGasUsed(); v != "" {
		out.GasUsed = &v
	}
	if v := in.GetGasPrice(); v != "" {
		out.GasPrice = &v
	}
	if v := in.GetTotalGasCost(); v != "" {
		out.TotalGasCost = &v
	}
	if inputs := in.GetInputs(); len(inputs) > 0 {
		out.Inputs = &inputs
	}
	if cfg := in.GetConfig(); cfg != nil {
		if m, ok := cfg.AsInterface().(map[string]interface{}); ok {
			out.Config = &m
		}
	}
	if md := in.GetMetadata(); md != nil {
		if m, ok := md.AsInterface().(map[string]interface{}); ok {
			out.Metadata = &m
		}
	}
	if ec := in.GetExecutionContext(); ec != nil {
		if m, ok := ec.AsInterface().(map[string]interface{}); ok {
			out.ExecutionContext = &m
		}
	}
	// Pull the per-variant OutputData payload via a protojson roundtrip
	// into a generic map (only the oneof variant key is read, so int64
	// quirks elsewhere don't matter).
	if raw, err := (protojson.MarshalOptions{EmitUnpopulated: false}).Marshal(in); err == nil {
		var envelope map[string]interface{}
		if json.Unmarshal(raw, &envelope) == nil {
			for _, key := range outputVariantKeys {
				if v, ok := envelope[key].(map[string]interface{}); ok {
					out.Output = &v
					break
				}
			}
		}
	}
	return out, nil
}

// outputVariantKeys mirrors the Execution_Step.OutputData oneof JSON
// names emitted by protojson — same list as the trigger/node Output
// types in the proto.
var outputVariantKeys = []string{
	"blockTrigger", "fixedTimeTrigger", "cronTrigger", "eventTrigger", "manualTrigger",
	"ethTransfer", "graphql", "contractRead", "contractWrite", "customCode", "restApi",
	"branch", "filter", "loop", "balance",
}

// normalizeStepType converts the engine's per-step type identifier
// (e.g. "NODE_TYPE_CUSTOM_CODE" / "TRIGGER_TYPE_CRON") into the
// lowercase camelCase form the OpenAPI spec uses (`customCode`,
// `cron`). Bare names pass through unchanged.
func normalizeStepType(t string) string {
	switch t {
	case "TRIGGER_TYPE_BLOCK":
		return "block"
	case "TRIGGER_TYPE_FIXED_TIME":
		return "fixedTime"
	case "TRIGGER_TYPE_CRON":
		return "cron"
	case "TRIGGER_TYPE_EVENT":
		return "event"
	case "TRIGGER_TYPE_MANUAL":
		return "manual"
	case "NODE_TYPE_ETH_TRANSFER":
		return "ethTransfer"
	case "NODE_TYPE_CONTRACT_WRITE":
		return "contractWrite"
	case "NODE_TYPE_CONTRACT_READ":
		return "contractRead"
	case "NODE_TYPE_GRAPHQL_QUERY":
		return "graphqlQuery"
	case "NODE_TYPE_REST_API":
		return "restApi"
	case "NODE_TYPE_BRANCH":
		return "branch"
	case "NODE_TYPE_FILTER":
		return "filter"
	case "NODE_TYPE_LOOP":
		return "loop"
	case "NODE_TYPE_CUSTOM_CODE":
		return "customCode"
	case "NODE_TYPE_BALANCE":
		return "balance"
	}
	return t
}

// executionStatusProtoToWire normalizes ExecutionStatus enum names to the
// lowercase wire vocabulary used by the OpenAPI spec.
func executionStatusProtoToWire(s avsproto.ExecutionStatus) string {
	switch s {
	case avsproto.ExecutionStatus_EXECUTION_STATUS_UNSPECIFIED:
		// Zero value — status not yet set; treat as in-flight.
		return "pending"
	case avsproto.ExecutionStatus_EXECUTION_STATUS_PENDING:
		return "pending"
	case avsproto.ExecutionStatus_EXECUTION_STATUS_WAITING:
		return "waiting"
	case avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS:
		return "success"
	case avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED:
		return "failed"
	case avsproto.ExecutionStatus_EXECUTION_STATUS_ERROR:
		return "error"
	default:
		// Every defined enum value has an explicit case above (guarded by the
		// mapping test). A value reaching here is an unknown/future enum — surface
		// it as "error" rather than masquerading it as in-flight "pending" (that
		// silent default is exactly what once hid WAITING).
		return "error"
	}
}
