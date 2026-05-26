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
	if v := in.GetIndex(); v != 0 {
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
// Output oneof is flattened into the OpenAPI `output` map; the simplest
// route is a protojson marshal of the entire step, then re-extracting
// the fields we care about — this lets future proto additions to
// Execution_Step show up in the response without changing this mapper.
func protoExecutionStepToOpenAPI(in *avsproto.Execution_Step) (generated.ExecutionStep, error) {
	raw, err := (protojson.MarshalOptions{}).Marshal(in)
	if err != nil {
		return generated.ExecutionStep{}, err
	}
	var out generated.ExecutionStep
	if err := json.Unmarshal(raw, &out); err != nil {
		return generated.ExecutionStep{}, err
	}
	return out, nil
}

// executionStatusProtoToWire normalizes ExecutionStatus enum names to the
// lowercase wire vocabulary used by the OpenAPI spec.
func executionStatusProtoToWire(s avsproto.ExecutionStatus) string {
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
