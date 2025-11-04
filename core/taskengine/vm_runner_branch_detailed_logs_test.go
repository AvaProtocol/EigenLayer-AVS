package taskengine

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
)

// TestBranchNode_DetailedExecutionLogs tests the execution logs with real scenario data
// Based on aggregator-sepolia.log where balance is 0 and the else condition is taken
func TestBranchNode_DetailedExecutionLogs(t *testing.T) {
	vm := NewVM()
	processor := NewBranchProcessor(vm)

	// Set up the real scenario from the log:
	// balance1.data contains tokens with balance 0
	balance1Data := []interface{}{
		map[string]interface{}{
			"balance":          "0",
			"balanceFormatted": "0",
			"decimals":         18,
			"name":             "Ether",
			"symbol":           "ETH",
		},
		map[string]interface{}{
			"balance":          "0",
			"balanceFormatted": "0",
			"decimals":         18,
			"name":             "Unknown Token",
			"symbol":           "UNKNOWN",
			"tokenAddress":     "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		},
	}

	// settings contains the pool and amount configuration
	settings := map[string]interface{}{
		"name":   "Test template",
		"amount": "1",
		"uniswapv3_pool": map[string]interface{}{
			"token1": map[string]interface{}{
				"id": "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
			},
		},
	}

	vm.AddVar("balance1", map[string]interface{}{
		"data": balance1Data,
	})
	vm.AddVar("settings", settings)

	// The real branch conditions from the log
	branchNode := &avsproto.BranchNode{
		Config: &avsproto.BranchNode_Config{
			Conditions: []*avsproto.BranchNode_Condition{
				{
					Id:         "0",
					Type:       "if",
					Expression: "{{balance1.data.find(token => token?.tokenAddress?.toLowerCase() === settings.uniswapv3_pool.token1.id.toLowerCase()).balance > Number(settings.amount)}}",
				},
				{
					Id:         "1",
					Type:       "else",
					Expression: "",
				},
			},
		},
	}

	// Execute the branch node
	step, nextStep, err := processor.Execute("branch1", branchNode)

	// Assertions
	assert.NoError(t, err, "Branch execution should not error")
	assert.NotNil(t, step, "Execution step should not be nil")
	assert.True(t, step.Success, "Branch should succeed by taking the else path")
	assert.NotNil(t, nextStep, "Next step should be set for else condition")

	// Check the log format
	log := step.Log
	t.Logf("Execution log:\n%s", log)

	// Verify log structure and content
	assert.Contains(t, log, "Executing NODE_TYPE_BRANCH", "Log should show node execution header")
	assert.Contains(t, log, "'branch1'", "Log should show node name")

	// The If condition should show as "resolved to false" with original expression
	assert.Contains(t, log, "If condition resolved to false", "Log should show 'If condition resolved to false'")
	assert.Contains(t, log, "Expression: {{balance1.data.find", "Log should show original template expression, not resolved value")

	// Should NOT show "Processed: false" (that's redundant)
	assert.NotContains(t, log, "Processed: false", "Log should NOT show redundant 'Processed: false' line")

	// The Else condition should be logged with next node information
	assert.Contains(t, log, "BranchNode 'branch1' resolved to Else condition", "Log should clearly state branch resolution")
	assert.Contains(t, log, "no next node", "Log should indicate no next node for simple test")

	// Verify step output data
	assert.NotNil(t, step.GetBranch(), "Branch output should be set")
	branchOutput := step.GetBranch()
	assert.NotNil(t, branchOutput.Data, "Branch data should be set")

	outputData := branchOutput.Data.AsInterface().(map[string]interface{})
	conditionId, ok := outputData["conditionId"].(string)
	assert.True(t, ok, "conditionId should be a string")
	assert.Equal(t, "branch1.1", conditionId, "Should take else condition (ID 1)")
}

// TestBranchNode_IfConditionTrue tests when the If condition evaluates to true
func TestBranchNode_IfConditionTrue(t *testing.T) {
	vm := NewVM()
	processor := NewBranchProcessor(vm)

	// Set up scenario where balance is greater than amount
	balance1Data := []interface{}{
		map[string]interface{}{
			"balance":          "1000000000000000000", // 1 token (18 decimals)
			"balanceFormatted": "1.0",
			"decimals":         18,
			"name":             "Test Token",
			"symbol":           "TEST",
			"tokenAddress":     "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		},
	}

	settings := map[string]interface{}{
		"name":   "Test template",
		"amount": "0.5",
		"uniswapv3_pool": map[string]interface{}{
			"token1": map[string]interface{}{
				"id": "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
			},
		},
	}

	vm.AddVar("balance1", map[string]interface{}{
		"data": balance1Data,
	})
	vm.AddVar("settings", settings)

	branchNode := &avsproto.BranchNode{
		Config: &avsproto.BranchNode_Config{
			Conditions: []*avsproto.BranchNode_Condition{
				{
					Id:         "0",
					Type:       "if",
					Expression: "{{balance1.data.find(token => token?.tokenAddress?.toLowerCase() === settings.uniswapv3_pool.token1.id.toLowerCase()).balance > Number(settings.amount)}}",
				},
				{
					Id:         "1",
					Type:       "else",
					Expression: "",
				},
			},
		},
	}

	// Execute the branch node
	step, nextStep, err := processor.Execute("branch1", branchNode)

	// Assertions
	assert.NoError(t, err, "Branch execution should not error")
	assert.NotNil(t, step, "Execution step should not be nil")
	assert.True(t, step.Success, "Branch should succeed by taking the if path")
	assert.NotNil(t, nextStep, "Next step should be set for if condition")

	// Check the log format
	log := step.Log
	t.Logf("Execution log:\n%s", log)

	// Verify log structure and content
	assert.Contains(t, log, "Executing NODE_TYPE_BRANCH", "Log should show node execution header")
	assert.Contains(t, log, "'branch1'", "Log should show node name")

	// The If condition should show evaluation result - since it's true, we should see resolution message
	assert.Contains(t, log, "BranchNode 'branch1' resolved to If condition", "Log should clearly state branch resolution")
	assert.Contains(t, log, "no next node", "Log should indicate no next node for simple test")

	// Verify step output data
	assert.NotNil(t, step.GetBranch(), "Branch output should be set")
	branchOutput := step.GetBranch()
	assert.NotNil(t, branchOutput.Data, "Branch data should be set")

	outputData := branchOutput.Data.AsInterface().(map[string]interface{})
	conditionId, ok := outputData["conditionId"].(string)
	assert.True(t, ok, "conditionId should be a string")
	assert.Equal(t, "branch1.0", conditionId, "Should take if condition (ID 0)")
}
