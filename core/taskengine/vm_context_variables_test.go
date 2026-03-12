package taskengine

import (
	"fmt"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- helpers ----------

// createTestVMForContext builds a VM with a real model.Task so that context
// is populated from actual task state (status, executionCount, etc.), not from
// inputVariables passthrough.
func createTestVMForContext(t *testing.T, status avsproto.TaskStatus, executionCount int64, lastRanAt int64, completedAt int64) *VM {
	t.Helper()

	node := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Lang:   avsproto.Lang_LANG_JAVASCRIPT,
			Source: "return true", // placeholder; overridden per test
		},
	}
	taskNode := &avsproto.TaskNode{
		Id:   "ctx_test_node",
		Name: "customJs",
		TaskType: &avsproto.TaskNode_CustomCode{
			CustomCode: node,
		},
	}
	trigger := &avsproto.TaskTrigger{Id: "trigger1", Name: "test_trigger"}
	edge := &avsproto.TaskEdge{Id: "e1", Source: trigger.Id, Target: taskNode.Id}

	task := &model.Task{
		Task: &avsproto.Task{
			Id:             "test-context-task",
			Status:         status,
			ExecutionCount: executionCount,
			LastRanAt:      lastRanAt,
			CompletedAt:    completedAt,
			Trigger:        trigger,
			Nodes:          []*avsproto.TaskNode{taskNode},
			Edges:          []*avsproto.TaskEdge{edge},
		},
	}

	vm, err := NewVMWithData(task, nil, testutil.GetTestSmartWalletConfig(), nil)
	require.NoError(t, err)
	return vm
}

// executeCustomCode runs a JS source on a VM that already has context populated
// from a real task, and returns the output as a map.
func executeCustomCode(t *testing.T, vm *VM, source string) map[string]interface{} {
	t.Helper()

	node := &avsproto.CustomCodeNode{
		Config: &avsproto.CustomCodeNode_Config{
			Lang:   avsproto.Lang_LANG_JAVASCRIPT,
			Source: source,
		},
	}

	p := NewJSProcessor(vm)
	step, err := p.Execute("ctx_test_node", node)
	require.NoError(t, err)
	require.True(t, step.Success, "JS execution failed: %s", step.Error)
	require.NotNil(t, step.GetCustomCode())
	require.NotNil(t, step.GetCustomCode().Data)

	out := step.GetCustomCode().Data.AsInterface()
	m, ok := out.(map[string]interface{})
	require.True(t, ok, "expected map output, got %T", out)
	return m
}

func createTestEngineForContext(t *testing.T) *Engine {
	t.Helper()
	logger := testutil.GetLogger()
	db := testutil.TestMustDB()
	t.Cleanup(func() { db.Close() })
	config := testutil.GetAggregatorConfig()
	return New(db, config, nil, logger)
}

// ==========================================================================
// 1. context.* runtime fields — template resolution ({{context.field}})
// ==========================================================================

func TestContextVariables_TemplateResolution(t *testing.T) {
	t.Run("context.status resolves to task status string", func(t *testing.T) {
		vm := createTestVMForContext(t, avsproto.TaskStatus_Enabled, 0, 0, 0)
		result := vm.preprocessTextWithVariableMapping("status is {{context.status}}")
		assert.Equal(t, "status is Enabled", result)
	})

	t.Run("context.executionCount resolves to count", func(t *testing.T) {
		vm := createTestVMForContext(t, avsproto.TaskStatus_Enabled, 7, 0, 0)
		result := vm.preprocessTextWithVariableMapping("count is {{context.executionCount}}")
		assert.Equal(t, "count is 7", result)
	})

	t.Run("context.lastRanAt resolves to timestamp", func(t *testing.T) {
		vm := createTestVMForContext(t, avsproto.TaskStatus_Enabled, 1, 1710000000, 0)
		result := vm.preprocessTextWithVariableMapping("last ran at {{context.lastRanAt}}")
		assert.Equal(t, "last ran at 1710000000", result)
	})

	t.Run("context.completedAt resolves to timestamp", func(t *testing.T) {
		vm := createTestVMForContext(t, avsproto.TaskStatus_Completed, 5, 1710000000, 1710001000)
		result := vm.preprocessTextWithVariableMapping("completed at {{context.completedAt}}")
		assert.Equal(t, "completed at 1710001000", result)
	})

	t.Run("all four context fields in single template", func(t *testing.T) {
		vm := createTestVMForContext(t, avsproto.TaskStatus_Completed, 3, 1710000000, 1710001000)
		result := vm.preprocessTextWithVariableMapping(
			"{{context.status}}/{{context.executionCount}}/{{context.lastRanAt}}/{{context.completedAt}}")
		assert.Equal(t, "Completed/3/1710000000/1710001000", result)
	})
}

// ==========================================================================
// 2. context.status reflects every TaskStatus enum value
// ==========================================================================

func TestContextVariables_StatusChanges(t *testing.T) {
	statuses := []struct {
		status   avsproto.TaskStatus
		expected string
	}{
		{avsproto.TaskStatus_Enabled, "Enabled"},
		{avsproto.TaskStatus_Disabled, "Disabled"},
		{avsproto.TaskStatus_Completed, "Completed"},
		{avsproto.TaskStatus_Failed, "Failed"},
		{avsproto.TaskStatus_Running, "Running"},
	}

	for _, tc := range statuses {
		t.Run("status_"+tc.expected, func(t *testing.T) {
			vm := createTestVMForContext(t, tc.status, 0, 0, 0)
			result := vm.preprocessTextWithVariableMapping("{{context.status}}")
			assert.Equal(t, tc.expected, result)
		})
	}
}

// ==========================================================================
// 3. context.* accessed in custom_code via JS variable — real task state
//    VM is created with NewVMWithData(task), so context comes from task fields,
//    NOT from inputVariables passthrough.
// ==========================================================================

func TestContextVariables_CustomCode_JSAccess(t *testing.T) {
	t.Run("all context fields accessible in JS from task state", func(t *testing.T) {
		vm := createTestVMForContext(t, avsproto.TaskStatus_Enabled, 5, 1710000000, 0)
		out := executeCustomCode(t, vm, `
			return {
				status: context.status,
				executionCount: context.executionCount,
				lastRanAt: context.lastRanAt,
				completedAt: context.completedAt,
			};
		`)
		assert.Equal(t, "Enabled", out["status"])
		assert.Equal(t, float64(5), out["executionCount"])
		assert.Equal(t, float64(1710000000), out["lastRanAt"])
		assert.Equal(t, float64(0), out["completedAt"])
	})

	t.Run("context.status is Disabled from task state", func(t *testing.T) {
		vm := createTestVMForContext(t, avsproto.TaskStatus_Disabled, 0, 0, 0)
		out := executeCustomCode(t, vm, `return { status: context.status };`)
		assert.Equal(t, "Disabled", out["status"])
	})

	t.Run("context.status is Completed from task state", func(t *testing.T) {
		vm := createTestVMForContext(t, avsproto.TaskStatus_Completed, 10, 1710001000, 1710001000)
		out := executeCustomCode(t, vm, `return { status: context.status };`)
		assert.Equal(t, "Completed", out["status"])
	})

	t.Run("context.status is Failed from task state", func(t *testing.T) {
		vm := createTestVMForContext(t, avsproto.TaskStatus_Failed, 3, 1710000500, 0)
		out := executeCustomCode(t, vm, `return { status: context.status };`)
		assert.Equal(t, "Failed", out["status"])
	})

	t.Run("context.status is Running from task state", func(t *testing.T) {
		vm := createTestVMForContext(t, avsproto.TaskStatus_Running, 1, 1710000100, 0)
		out := executeCustomCode(t, vm, `return { status: context.status };`)
		assert.Equal(t, "Running", out["status"])
	})
}

// ==========================================================================
// 4. context.* used via {{template}} syntax in custom_code source
// ==========================================================================

func TestContextVariables_CustomCode_TemplateInSource(t *testing.T) {
	t.Run("template substitution in JS source from task state", func(t *testing.T) {
		vm := createTestVMForContext(t, avsproto.TaskStatus_Running, 42, 0, 0)
		out := executeCustomCode(t, vm, `
			var s = "{{context.status}}";
			var c = "{{context.executionCount}}";
			return { statusStr: s, countStr: c };
		`)
		assert.Equal(t, "Running", out["statusStr"])
		assert.Equal(t, "42", out["countStr"])
	})
}

// ==========================================================================
// 5. context.status mutation — simulate executor.go updating context mid-run
// ==========================================================================

func TestContextVariables_StatusMutation(t *testing.T) {
	t.Run("status changes from Enabled to Completed after mutation", func(t *testing.T) {
		vm := createTestVMForContext(t, avsproto.TaskStatus_Enabled, 0, 0, 0)

		// Initially Enabled
		result := vm.preprocessTextWithVariableMapping("{{context.status}}")
		assert.Equal(t, "Enabled", result)

		// Simulate status change (as executor.go does after execution completes)
		vm.mu.Lock()
		if ctx, ok := vm.vars[ContextVarName].(map[string]interface{}); ok {
			ctx["status"] = "Completed"
			ctx["executionCount"] = int64(1)
			ctx["completedAt"] = int64(1710001000)
		}
		vm.mu.Unlock()

		// After mutation — template resolution picks up new values
		assert.Equal(t, "Completed", vm.preprocessTextWithVariableMapping("{{context.status}}"))
		assert.Equal(t, "1", vm.preprocessTextWithVariableMapping("{{context.executionCount}}"))
		assert.Equal(t, "1710001000", vm.preprocessTextWithVariableMapping("{{context.completedAt}}"))
	})

	t.Run("status mutation visible to JS execution", func(t *testing.T) {
		vm := createTestVMForContext(t, avsproto.TaskStatus_Enabled, 0, 0, 0)

		// Verify initial state in JS
		out := executeCustomCode(t, vm, `return { status: context.status };`)
		assert.Equal(t, "Enabled", out["status"])

		// Mutate status (as executor.go does)
		vm.mu.Lock()
		if ctx, ok := vm.vars[ContextVarName].(map[string]interface{}); ok {
			ctx["status"] = "Failed"
			ctx["executionCount"] = int64(1)
		}
		vm.mu.Unlock()

		// JS sees updated value
		out = executeCustomCode(t, vm, `return { status: context.status, count: context.executionCount };`)
		assert.Equal(t, "Failed", out["status"])
		assert.Equal(t, float64(1), out["count"])
	})

	t.Run("executionCount increments across runs", func(t *testing.T) {
		vm := createTestVMForContext(t, avsproto.TaskStatus_Enabled, 0, 0, 0)

		for i := int64(1); i <= 3; i++ {
			vm.mu.Lock()
			if ctx, ok := vm.vars[ContextVarName].(map[string]interface{}); ok {
				ctx["executionCount"] = i
			}
			vm.mu.Unlock()

			result := vm.preprocessTextWithVariableMapping("{{context.executionCount}}")
			assert.Equal(t, fmt.Sprintf("%d", i), result)
		}
	})

	t.Run("status transitions Enabled -> Running -> Failed", func(t *testing.T) {
		vm := createTestVMForContext(t, avsproto.TaskStatus_Enabled, 0, 0, 0)

		transitions := []string{"Enabled", "Running", "Failed"}
		for _, expected := range transitions {
			vm.mu.Lock()
			if ctx, ok := vm.vars[ContextVarName].(map[string]interface{}); ok {
				ctx["status"] = expected
			}
			vm.mu.Unlock()

			result := vm.preprocessTextWithVariableMapping("{{context.status}}")
			assert.Equal(t, expected, result)
		}
	})
}

// ==========================================================================
// 6. Arbitrary inputVariables (top-level user-defined vars, NOT context)
//    These use RunNodeImmediately because that's the real path for user-defined
//    inputVariables — they're passed in and added to the VM at run_node_immediately.go:3076.
// ==========================================================================

func TestInputVariables_ArbitraryTopLevel_CustomCode(t *testing.T) {
	engine := createTestEngineForContext(t)

	t.Run("top-level inputVariable accessible in JS", func(t *testing.T) {
		config := map[string]interface{}{
			"lang":   "JavaScript",
			"source": `return { key: myApiKey, addr: recipientAddr };`,
		}
		inputVariables := map[string]interface{}{
			"myApiKey":      "sk-test-12345",
			"recipientAddr": "0x1234567890abcdef1234567890abcdef12345678",
		}

		result, err := engine.RunNodeImmediately(NodeTypeCustomCode, config, inputVariables, nil)
		require.NoError(t, err)
		assert.Equal(t, "sk-test-12345", result["key"])
		assert.Equal(t, "0x1234567890abcdef1234567890abcdef12345678", result["addr"])
	})

	t.Run("top-level inputVariable accessible via template", func(t *testing.T) {
		config := map[string]interface{}{
			"lang":   "JavaScript",
			"source": `return { msg: "key={{myApiKey}}" };`,
		}
		inputVariables := map[string]interface{}{
			"myApiKey": "sk-test-99999",
		}

		result, err := engine.RunNodeImmediately(NodeTypeCustomCode, config, inputVariables, nil)
		require.NoError(t, err)
		assert.Equal(t, "key=sk-test-99999", result["msg"])
	})
}

// ==========================================================================
// 7. context.* in contract_read methodParams via template substitution
// ==========================================================================

func TestContextVariables_ContractRead_MethodParams(t *testing.T) {
	engine := createTestEngineForContext(t)

	t.Run("context.executionCount in contractRead methodParams resolves", func(t *testing.T) {
		// Template resolution happens before the RPC call.
		// The call itself may fail (no chain), but we verify no "undefined" error.
		config := map[string]interface{}{
			"contractAddress": "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
			"contractAbi":     `[{"inputs":[{"name":"offset","type":"uint256"}],"name":"balanceAt","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"}]`,
			"methodName":      "balanceAt",
			"methodParams":    []interface{}{"{{context.executionCount}}"},
		}
		inputVariables := map[string]interface{}{
			"context": map[string]interface{}{
				"status":         "Enabled",
				"executionCount": int64(42),
				"lastRanAt":      int64(0),
				"completedAt":    int64(0),
			},
		}

		_, err := engine.RunNodeImmediately(NodeTypeContractRead, config, inputVariables, nil)
		if err != nil {
			assert.NotContains(t, err.Error(), "could not resolve variable context")
			assert.NotContains(t, err.Error(), "undefined")
		}
	})
}

// ==========================================================================
// 8. Arbitrary inputVariables in contract_read methodParams
// ==========================================================================

func TestInputVariables_ArbitraryTopLevel_ContractRead(t *testing.T) {
	engine := createTestEngineForContext(t)

	t.Run("top-level inputVariable in contractRead methodParams resolves", func(t *testing.T) {
		config := map[string]interface{}{
			"contractAddress": "0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48",
			"contractAbi":     `[{"inputs":[{"name":"account","type":"address"}],"name":"balanceOf","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"}]`,
			"methodName":      "balanceOf",
			"methodParams":    []interface{}{"{{targetAddress}}"},
		}
		inputVariables := map[string]interface{}{
			"targetAddress": "0x1234567890abcdef1234567890abcdef12345678",
		}

		_, err := engine.RunNodeImmediately(NodeTypeContractRead, config, inputVariables, nil)
		if err != nil {
			assert.NotContains(t, err.Error(), "could not resolve variable targetAddress")
			assert.NotContains(t, err.Error(), "undefined")
		}
	})
}

// ==========================================================================
// 9. context.* combined with arbitrary inputVariables in custom_code
// ==========================================================================

func TestContextVariables_CombinedWithOtherVars(t *testing.T) {
	t.Run("context from task state and arbitrary vars coexist", func(t *testing.T) {
		vm := createTestVMForContext(t, avsproto.TaskStatus_Enabled, 10, 0, 0)

		// Add an arbitrary top-level variable alongside task-populated context
		vm.AddVar("threshold", int64(5))

		out := executeCustomCode(t, vm, `
			return {
				status: context.status,
				count: context.executionCount,
				threshold: threshold,
				shouldStop: context.executionCount >= threshold,
			};
		`)
		assert.Equal(t, "Enabled", out["status"])
		assert.Equal(t, float64(10), out["count"])
		assert.Equal(t, float64(5), out["threshold"])
		assert.Equal(t, true, out["shouldStop"])
	})
}
