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

	// Verify text format includes evaluation details
	// Note: step name shows as "unknown" because the step isn't in a full task context
	assert.Contains(t, text, "selected Else condition", "Text should show selected condition")
	assert.Contains(t, text, "If: false", "Text should show If condition was false")
	assert.Contains(t, text, "Else (selected)", "Text should show Else was selected")

	// Verify HTML format matches execution log format
	assert.Contains(t, html, "selected Else condition", "HTML should show selected condition")
	assert.Contains(t, html, "-&gt; no next node", "HTML should show next node info (HTML encoded)")
	assert.Contains(t, html, "If condition resolved to false", "HTML should show 'If condition resolved to false'")
	assert.Contains(t, html, "balance1.data.find", "HTML should show original expression")
	assert.Contains(t, html, "Expression:", "HTML should use execution log format with 'Expression:' label")

	// Verify the HTML uses the improved format (showing original expression, not resolved value)
	assert.NotContains(t, html, "Expression: false", "HTML should NOT show just 'false' as the expression")
	// Note: The expression contains HTML entities like &gt;
	assert.Contains(t, html, "Expression: {{balance1.data.find", "HTML should show the template expression")
}
