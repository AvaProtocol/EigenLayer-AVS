package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
)

// TestBranchNode_EmailSummaryGeneration tests that branch evaluation details
// are properly stored in metadata and used to generate improved email HTML
func TestBranchNode_EmailSummaryGeneration(t *testing.T) {
	// Create VM with a branch node
	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id: "test-task",
			Trigger: &avsproto.TaskTrigger{
				Id:   "test-trigger",
				Name: "test",
				TriggerType: &avsproto.TaskTrigger_Manual{
					Manual: &avsproto.ManualTrigger{
						Config: &avsproto.ManualTrigger_Config{},
					},
				},
			},
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	assert.NoError(t, err, "Failed to create VM")

	// Set up the real scenario: balance is 0, so If condition should fail
	balance1Data := []interface{}{
		map[string]interface{}{
			"balance":          "0",
			"balanceFormatted": "0",
			"decimals":         18,
			"name":             "Test Token",
			"symbol":           "TEST",
			"tokenAddress":     "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		},
	}

	settings := map[string]interface{}{
		"name":   "Test Workflow",
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

	processor := NewBranchProcessor(vm)

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

	// Create TaskNode wrapper for the branch node
	taskNode := &avsproto.TaskNode{
		Id:   "branch1",
		Name: "branch1",
		Type: avsproto.NodeType_NODE_TYPE_BRANCH,
		TaskType: &avsproto.TaskNode_Branch{
			Branch: branchNode,
		},
	}
	processor.CommonProcessor.SetTaskNode(taskNode)

	// Execute the branch node
	step, _, err := processor.Execute("branch1", branchNode)
	assert.NoError(t, err, "Branch execution should not error")
	assert.True(t, step.Success, "Branch should succeed")

	// Verify metadata contains evaluation details
	assert.NotNil(t, step.Metadata, "Metadata should be set")
	metaMap, ok := step.Metadata.AsInterface().(map[string]interface{})
	assert.True(t, ok, "Metadata should be a map")

	evals, ok := metaMap["conditionEvaluations"].([]interface{})
	assert.True(t, ok, "conditionEvaluations should exist in metadata")
	assert.Equal(t, 2, len(evals), "Should have 2 condition evaluations")

	// Check first evaluation (If: false)
	eval0, ok := evals[0].(map[string]interface{})
	assert.True(t, ok, "First evaluation should be a map")
	assert.Equal(t, "If", eval0["label"], "First condition should be labeled 'If'")
	assert.Equal(t, false, eval0["result"], "First condition should evaluate to false")
	assert.Equal(t, false, eval0["taken"], "First condition should not be taken")
	assert.Contains(t, eval0["expression"], "balance1.data.find", "Expression should be the original template")

	// Verify comparison operands are extracted (not empty object)
	varVals, hasVarVals := eval0["variableValues"].(map[string]interface{})
	assert.True(t, hasVarVals, "variableValues should exist")
	assert.NotNil(t, varVals, "variableValues should not be nil")

	// Check if it has comparison operand structure
	if len(varVals) > 0 {
		t.Logf("variableValues content: %+v", varVals)
		// Should have leftExpr, rightExpr, operator, left, right keys
		if leftExpr, ok := varVals["leftExpr"].(string); ok {
			assert.NotEmpty(t, leftExpr, "leftExpr should not be empty")
			assert.Contains(t, leftExpr, "balance1.data.find", "leftExpr should contain the left side of comparison")
			t.Logf("✅ leftExpr: %s", leftExpr)
		}
		if rightExpr, ok := varVals["rightExpr"].(string); ok {
			assert.NotEmpty(t, rightExpr, "rightExpr should not be empty")
			assert.Contains(t, rightExpr, "settings.amount", "rightExpr should contain the right side of comparison")
			t.Logf("✅ rightExpr: %s", rightExpr)
		}
		if operator, ok := varVals["operator"].(string); ok {
			assert.Equal(t, ">", operator, "operator should be '>'")
			t.Logf("✅ operator: %s", operator)
		}
		// Check evaluated values
		if left := varVals["left"]; left != nil {
			t.Logf("✅ left operand value: %v", left)
		}
		if right := varVals["right"]; right != nil {
			t.Logf("✅ right operand value: %v", right)
		}
	} else {
		t.Error("❌ variableValues is empty - comparison operands were not extracted!")
	}

	// Check second evaluation (Else: taken)
	eval1, ok := evals[1].(map[string]interface{})
	assert.True(t, ok, "Second evaluation should be a map")
	assert.Equal(t, "Else", eval1["label"], "Second condition should be labeled 'Else'")
	assert.Equal(t, true, eval1["result"], "Else condition should result in true")
	assert.Equal(t, true, eval1["taken"], "Else condition should be taken")

	// Add step to VM execution logs
	vm.ExecutionLogs = append(vm.ExecutionLogs, step)

	// Generate email summary HTML
	text, html := BuildBranchAndSkippedSummary(vm)

	t.Logf("Generated text summary:\n%s", text)
	t.Logf("Generated HTML summary:\n%s", html)

	// NOTE: For successful workflow runs, we don't include detailed branch condition explanations
	// in the email summary. Branch conditions are only shown in detail when debugging failures.
	// For success cases, the summary just shows "All nodes were executed successfully".
	// The branch metadata is still captured for debugging but not displayed in success summaries.

	// Verify the summary shows successful execution
	assert.Contains(t, text, "All nodes were executed successfully", "Text should show successful execution")
	assert.Contains(t, html, "All nodes were executed successfully", "HTML should show successful execution")
}

// TestBranchNode_TrueConditionLogging tests that true conditions also log comparison operands
func TestBranchNode_TrueConditionLogging(t *testing.T) {
	// Create VM with a branch node
	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id: "test-task",
			Trigger: &avsproto.TaskTrigger{
				Id:   "test-trigger",
				Name: "test",
				TriggerType: &avsproto.TaskTrigger_Manual{
					Manual: &avsproto.ManualTrigger{
						Config: &avsproto.ManualTrigger_Config{},
					},
				},
			},
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	assert.NoError(t, err, "Failed to create VM")

	// Set up scenario where If condition is TRUE (balance > amount)
	balance1Data := []interface{}{
		map[string]interface{}{
			"balance":          "20000000", // 20M - greater than 10000
			"balanceFormatted": "20000000",
			"decimals":         18,
			"name":             "Test Token",
			"symbol":           "TEST",
			"tokenAddress":     "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
		},
	}

	settings := map[string]interface{}{
		"name":   "Test Workflow",
		"amount": "10000",
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

	processor := NewBranchProcessor(vm)

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

	// Create TaskNode wrapper for the branch node
	taskNode := &avsproto.TaskNode{
		Id:   "branch1",
		Name: "branch1",
		Type: avsproto.NodeType_NODE_TYPE_BRANCH,
		TaskType: &avsproto.TaskNode_Branch{
			Branch: branchNode,
		},
	}
	processor.CommonProcessor.SetTaskNode(taskNode)

	// Execute the branch node
	step, _, err := processor.Execute("branch1", branchNode)
	assert.NoError(t, err, "Branch execution should not error")
	assert.True(t, step.Success, "Branch should succeed")

	// Add the step to ExecutionLogs so BuildBranchAndSkippedSummary can find it
	vm.ExecutionLogs = append(vm.ExecutionLogs, step)

	// Verify metadata contains evaluation details with operand values
	assert.NotNil(t, step.Metadata, "Metadata should be set")
	metaMap, ok := step.Metadata.AsInterface().(map[string]interface{})
	assert.True(t, ok, "Metadata should be a map")

	evals, ok := metaMap["conditionEvaluations"].([]interface{})
	assert.True(t, ok, "conditionEvaluations should exist in metadata")
	assert.Equal(t, 1, len(evals), "Should have 1 condition evaluation (only If, not Else)")

	// Check the If evaluation (true and taken)
	eval0, ok := evals[0].(map[string]interface{})
	assert.True(t, ok, "Evaluation should be a map")
	assert.Equal(t, "If", eval0["label"], "Should be labeled 'If'")
	assert.Equal(t, true, eval0["result"], "If condition should evaluate to true")
	assert.Equal(t, true, eval0["taken"], "If condition should be taken")

	// Verify comparison operands are extracted for true conditions too
	varVals, hasVarVals := eval0["variableValues"].(map[string]interface{})
	assert.True(t, hasVarVals, "variableValues should exist for true conditions")
	assert.NotNil(t, varVals, "variableValues should not be nil")
	assert.NotEmpty(t, varVals, "variableValues should not be empty for true conditions")

	if len(varVals) > 0 {
		t.Logf("variableValues for TRUE condition: %+v", varVals)
		leftExpr, _ := varVals["leftExpr"].(string)
		rightExpr, _ := varVals["rightExpr"].(string)
		operator, _ := varVals["operator"].(string)

		assert.Contains(t, leftExpr, "balance1.data.find", "leftExpr should contain balance1 expression")
		assert.Contains(t, rightExpr, "settings.amount", "rightExpr should contain settings.amount")
		assert.Equal(t, ">", operator, "operator should be '>'")

		t.Logf("✅ True condition operands - left: %v, right: %v", varVals["left"], varVals["right"])
	}

	// Verify execution log shows comparison details for TRUE conditions
	log := step.Log
	t.Logf("Execution log:\n%s", log)

	assert.Contains(t, log, "If condition resolved to true", "Log should show 'If condition resolved to true'")
	assert.Contains(t, log, "Expression:", "Log should show 'Expression:' prefix for consistency")
	assert.Contains(t, log, "balance1.data.find", "Log should show the left operand expression")
	assert.Contains(t, log, "Number(settings.amount)", "Log should show the right operand expression")
	assert.Contains(t, log, "Evaluated:", "Log should show 'Evaluated:' with operand values")
	assert.Contains(t, log, ">", "Log should show the comparison operator")

	// Should show evaluated values, not entire variable dumps
	assert.NotContains(t, log, `"balance":"20000000"`, "Log should NOT dump the entire balance1 object")
	assert.NotContains(t, log, `"name":"Test Workflow"`, "Log should NOT dump the entire settings object")

	// Verify HTML email summary for successful workflows
	_, htmlSummary := BuildBranchAndSkippedSummary(vm)
	t.Logf("HTML summary:\n%s", htmlSummary)

	// NOTE: For successful workflow runs, we don't include detailed branch condition explanations
	// in the email summary. Branch conditions are only shown in detail when debugging failures.
	// For success cases, the summary just shows "All nodes were executed successfully".

	// Verify the summary shows successful execution
	assert.Contains(t, htmlSummary, "All nodes were executed successfully", "HTML should show successful execution")
}
