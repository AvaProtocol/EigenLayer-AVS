package taskengine

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestPropagateLoopExecutionContext verifies that a loop step inherits the
// ExecutionContext from its first iteration step that has one. This is what
// makes a loop wrapping Tenderly-simulated contract writes correctly report
// `is_simulated: true, provider: tenderly` instead of the misleading
// `chain_rpc` default that CreateNodeExecutionStep would otherwise leave behind.
func TestPropagateLoopExecutionContext(t *testing.T) {
	mkCtx := func(simulated bool, provider string) *structpb.Value {
		v, err := structpb.NewValue(map[string]interface{}{
			"is_simulated": simulated,
			"provider":     provider,
			"chain_id":     11155111,
		})
		if err != nil {
			t.Fatalf("structpb.NewValue: %v", err)
		}
		return v
	}

	t.Run("propagates from first iteration with context", func(t *testing.T) {
		parent := &avsproto.Execution_Step{Id: "loop1"}
		iter0 := &avsproto.Execution_Step{Id: "iter0", ExecutionContext: mkCtx(true, "tenderly")}
		iter1 := &avsproto.Execution_Step{Id: "iter1", ExecutionContext: mkCtx(true, "tenderly")}

		propagateLoopExecutionContext(parent, []*avsproto.Execution_Step{iter0, iter1})

		if parent.ExecutionContext == nil {
			t.Fatal("expected parent ExecutionContext to be set")
		}
		ctxMap := parent.ExecutionContext.GetStructValue().AsMap()
		if got := ctxMap["is_simulated"]; got != true {
			t.Errorf("is_simulated = %v, want true", got)
		}
		if got := ctxMap["provider"]; got != "tenderly" {
			t.Errorf("provider = %v, want tenderly", got)
		}
	})

	t.Run("skips nil iterations and picks first non-nil context", func(t *testing.T) {
		parent := &avsproto.Execution_Step{Id: "loop1"}
		iter0 := &avsproto.Execution_Step{Id: "iter0"} // no ExecutionContext
		iter1 := (*avsproto.Execution_Step)(nil)
		iter2 := &avsproto.Execution_Step{Id: "iter2", ExecutionContext: mkCtx(false, "bundler")}

		propagateLoopExecutionContext(parent, []*avsproto.Execution_Step{iter0, iter1, iter2})

		if parent.ExecutionContext == nil {
			t.Fatal("expected parent ExecutionContext to be set from iter2")
		}
		ctxMap := parent.ExecutionContext.GetStructValue().AsMap()
		if got := ctxMap["provider"]; got != "bundler" {
			t.Errorf("provider = %v, want bundler", got)
		}
		if got := ctxMap["is_simulated"]; got != false {
			t.Errorf("is_simulated = %v, want false", got)
		}
	})

	t.Run("leaves parent untouched when no iteration has context", func(t *testing.T) {
		baseline := mkCtx(false, "chain_rpc")
		parent := &avsproto.Execution_Step{Id: "loop1", ExecutionContext: baseline}
		iter0 := &avsproto.Execution_Step{Id: "iter0"}

		propagateLoopExecutionContext(parent, []*avsproto.Execution_Step{iter0})

		if parent.ExecutionContext != baseline {
			t.Error("expected parent ExecutionContext to remain unchanged when no iteration provides one")
		}
	})

	t.Run("handles empty iterations slice", func(t *testing.T) {
		parent := &avsproto.Execution_Step{Id: "loop1"}
		propagateLoopExecutionContext(parent, nil)
		if parent.ExecutionContext != nil {
			t.Error("expected parent ExecutionContext to remain nil for empty iterations")
		}
	})

	t.Run("handles nil parent gracefully", func(t *testing.T) {
		// Should not panic.
		propagateLoopExecutionContext(nil, []*avsproto.Execution_Step{
			{ExecutionContext: mkCtx(true, "tenderly")},
		})
	})
}
