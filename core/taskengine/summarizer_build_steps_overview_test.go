package taskengine

import (
	"fmt"
	"strings"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestBuildStepsOverview_SimulatedPrefix(t *testing.T) {
	vm := NewVM()
	vm.mu.Lock()
	vm.vars["settings"] = map[string]interface{}{
		"uniswapv3_pool": map[string]interface{}{
			"token0": map[string]interface{}{
				"symbol": "USDC",
			},
			"token1": map[string]interface{}{
				"symbol": "USDT",
			},
		},
		"uniswapv3_contracts": map[string]interface{}{
			"swapRouter02": "0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e",
		},
	}
	vm.mu.Unlock()

	// Create a simulated exactInputSingle step
	metadataValue, err := structpb.NewValue([]interface{}{
		map[string]interface{}{
			"value": map[string]interface{}{
				"amountOut": "1833869241732629",
			},
		},
	})
	require.NoError(t, err)

	configValue, err := structpb.NewValue(map[string]interface{}{
		"contractAddress": "0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e",
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName": "exactInputSingle",
			},
		},
	})
	require.NoError(t, err)

	executionContextValue, err := structpb.NewValue(map[string]interface{}{
		"isSimulated": true,
		"chainId":     11155111, // Sepolia
		"provider":    "simulation",
	})
	require.NoError(t, err)

	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id:               "swap1",
			Name:             "swap1",
			Success:          true,
			Type:             avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(),
			Config:           configValue,
			Metadata:         metadataValue,
			ExecutionContext: executionContextValue,
		},
	}

	result := buildStepsOverview(vm)

	// Check that "(Simulated)" appears as a prefix, not suffix
	if !strings.Contains(result, "✓ (Simulated)") {
		t.Fatalf("expected '(Simulated)' prefix, got: %q", result)
	}

	// Check that "(Simulated)" is NOT at the end
	if strings.HasSuffix(result, "(Simulated)") {
		t.Fatalf("'(Simulated)' should be a prefix, not suffix. Got: %q", result)
	}

	// Check that amount is formatted (should contain decimal point)
	// USDC has 6 decimals, so 1833869241732629 / 10^6 = 1833869241.732629
	if !strings.Contains(result, ".") {
		t.Fatalf("expected formatted amount with decimal point, got: %q", result)
	}
	// Should not contain the raw number
	if strings.Contains(result, "1833869241732629") {
		t.Fatalf("expected formatted amount, not raw number, got: %q", result)
	}

	// Check that token symbol is included
	if !strings.Contains(result, "USDC") {
		t.Fatalf("expected token symbol USDC, got: %q", result)
	}
}

func TestBuildStepsOverview_ApproveWithTokenSymbol(t *testing.T) {
	vm := NewVM()
	vm.mu.Lock()
	vm.vars["settings"] = map[string]interface{}{
		"uniswapv3_pool": map[string]interface{}{
			"token1": map[string]interface{}{
				"symbol": "USDT",
			},
		},
		"uniswapv3_contracts": map[string]interface{}{
			"swapRouter02": "0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e",
		},
	}
	vm.mu.Unlock()

	// Create Approval event data
	approvalData, err := structpb.NewValue(map[string]interface{}{
		"Approval": map[string]interface{}{
			"owner":   "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
			"spender": "0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e",
			"value":   "1000000", // 1 USDT with 6 decimals
		},
	})
	require.NoError(t, err)

	configValue, err := structpb.NewValue(map[string]interface{}{
		"contractAddress": "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238", // Token address
		"isSimulated":     false,
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName": "approve",
			},
		},
	})
	require.NoError(t, err)

	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id:      "approve1",
			Name:    "approve1",
			Success: true,
			Type:    avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(),
			Config:  configValue,
			OutputData: &avsproto.Execution_Step_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode_Output{
					Data: approvalData,
				},
			},
		},
	}

	result := buildStepsOverview(vm)

	// Check that token symbol is included
	if !strings.Contains(result, "USDT") {
		t.Fatalf("expected token symbol USDT, got: %q", result)
	}

	// Check that amount is formatted
	if !strings.Contains(result, "1.0") || !strings.Contains(result, "1.0000") {
		t.Fatalf("expected formatted amount (1.0 or 1.0000), got: %q", result)
	}

	// Check that spender address is included (Uniswap V3 router)
	if !strings.Contains(result, "Uniswap V3 router") {
		t.Fatalf("expected 'Uniswap V3 router' in description, got: %q", result)
	}

	// Check that template variables are NOT present
	if strings.Contains(result, "{{") {
		t.Fatalf("expected no template variables, got: %q", result)
	}
}

func TestBuildStepsOverview_RealAndSimulatedTransactions(t *testing.T) {
	vm := NewVM()
	vm.mu.Lock()
	vm.vars["settings"] = map[string]interface{}{
		"uniswapv3_pool": map[string]interface{}{
			"token0": map[string]interface{}{
				"symbol": "USDC",
			},
			"token1": map[string]interface{}{
				"symbol": "USDT",
			},
		},
		"uniswapv3_contracts": map[string]interface{}{
			"swapRouter02": "0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e",
		},
	}
	vm.mu.Unlock()

	// Create Approval event data
	approvalData, err := structpb.NewValue(map[string]interface{}{
		"Approval": map[string]interface{}{
			"owner":   "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
			"spender": "0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e",
			"value":   "1000000", // 1 USDT with 6 decimals
		},
	})
	require.NoError(t, err)

	approveConfig, err := structpb.NewValue(map[string]interface{}{
		"contractAddress": "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238",
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName": "approve",
			},
		},
	})
	require.NoError(t, err)

	approveExecutionContext, err := structpb.NewValue(map[string]interface{}{
		"isSimulated": false,    // Real transaction
		"chainId":     11155111, // Sepolia
		"provider":    "bundler",
	})
	require.NoError(t, err)

	// Create swap metadata
	swapMetadata, err := structpb.NewValue([]interface{}{
		map[string]interface{}{
			"value": map[string]interface{}{
				"amountOut": "1833869241732629",
			},
		},
	})
	require.NoError(t, err)

	swapConfig, err := structpb.NewValue(map[string]interface{}{
		"contractAddress": "0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e",
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName": "exactInputSingle",
			},
		},
	})
	require.NoError(t, err)

	swapExecutionContext, err := structpb.NewValue(map[string]interface{}{
		"isSimulated": true,     // Simulated transaction
		"chainId":     11155111, // Sepolia
		"provider":    "simulation",
	})
	require.NoError(t, err)

	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id:               "approve1",
			Name:             "approve1",
			Success:          true,
			Type:             avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(),
			Config:           approveConfig,
			ExecutionContext: approveExecutionContext,
			OutputData: &avsproto.Execution_Step_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode_Output{
					Data: approvalData,
				},
			},
		},
		{
			Id:               "swap1",
			Name:             "swap1",
			Success:          true,
			Type:             avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(),
			Config:           swapConfig,
			Metadata:         swapMetadata,
			ExecutionContext: swapExecutionContext,
		},
	}

	result := buildStepsOverview(vm)

	// Split by newlines to check each step
	lines := strings.Split(result, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least 2 steps, got %d lines: %q", len(lines), result)
	}

	// Check approve step (should NOT have Simulated prefix)
	approveLine := ""
	for _, line := range lines {
		if strings.Contains(line, "Approved") {
			approveLine = line
			break
		}
	}
	if approveLine == "" {
		t.Fatalf("expected approve step, got: %q", result)
	}
	if strings.Contains(approveLine, "(Simulated)") {
		t.Fatalf("approve step should NOT have (Simulated) prefix, got: %q", approveLine)
	}
	if !strings.Contains(approveLine, "USDT") {
		t.Fatalf("expected USDT in approve step, got: %q", approveLine)
	}

	// Check swap step (should HAVE Simulated prefix)
	swapLine := ""
	for _, line := range lines {
		if strings.Contains(line, "Swapped") {
			swapLine = line
			break
		}
	}
	if swapLine == "" {
		t.Fatalf("expected swap step, got: %q", result)
	}
	if !strings.Contains(swapLine, "(Simulated)") {
		t.Fatalf("swap step should have (Simulated) prefix, got: %q", swapLine)
	}
	// Check that (Simulated) is at the beginning (after checkmark)
	if !strings.Contains(swapLine, "✓ (Simulated)") {
		t.Fatalf("expected '(Simulated)' prefix after checkmark, got: %q", swapLine)
	}
	// Check that amount is formatted (should contain decimal point)
	// USDC has 6 decimals, so 1833869241732629 / 10^6 = 1833869241.732629
	if !strings.Contains(swapLine, ".") {
		t.Fatalf("expected formatted amount with decimal point, got: %q", swapLine)
	}
	// Should not contain the raw number
	if strings.Contains(swapLine, "1833869241732629") {
		t.Fatalf("expected formatted amount, not raw number, got: %q", swapLine)
	}
	if !strings.Contains(swapLine, "USDC") {
		t.Fatalf("expected USDC in swap step, got: %q", swapLine)
	}
}

func TestBuildBranchAndSkippedSummary_IncludesStepsOverview(t *testing.T) {
	vm := NewVM()
	vm.mu.Lock()
	vm.vars["settings"] = map[string]interface{}{
		"uniswapv3_pool": map[string]interface{}{
			"token0": map[string]interface{}{
				"symbol": "USDC",
			},
			"token1": map[string]interface{}{
				"symbol": "USDT",
			},
		},
		"uniswapv3_contracts": map[string]interface{}{
			"swapRouter02": "0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e",
		},
	}
	vm.mu.Unlock()

	// Create swap metadata
	swapMetadata, err := structpb.NewValue([]interface{}{
		map[string]interface{}{
			"value": map[string]interface{}{
				"amountOut": "1833869241732629",
			},
		},
	})
	require.NoError(t, err)

	swapConfig, err := structpb.NewValue(map[string]interface{}{
		"contractAddress": "0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e",
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName": "exactInputSingle",
			},
		},
	})
	require.NoError(t, err)

	swapExecutionContext, err := structpb.NewValue(map[string]interface{}{
		"isSimulated": true,
		"chainId":     11155111, // Sepolia
		"provider":    "simulation",
	})
	require.NoError(t, err)

	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id:               "swap1",
			Name:             "swap1",
			Success:          true,
			Type:             avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(),
			Config:           swapConfig,
			Metadata:         swapMetadata,
			ExecutionContext: swapExecutionContext,
		},
	}

	// Call BuildBranchAndSkippedSummary (which calls buildStepsOverview internally)
	text, html := BuildBranchAndSkippedSummary(vm)

	// Check that the HTML contains the "What Executed Successfully" section
	if !strings.Contains(html, "What Executed Successfully") {
		t.Fatalf("expected 'What Executed Successfully' section in HTML, got: %q", html)
	}

	// Check that (Simulated) appears as a prefix in the HTML
	if !strings.Contains(html, "(Simulated) Swapped") {
		t.Fatalf("expected '(Simulated)' prefix before 'Swapped' in HTML, got: %q", html)
	}

	// Check that amount is formatted (contains decimal point)
	if !strings.Contains(html, ".") {
		t.Fatalf("expected formatted amount with decimal point in HTML, got: %q", html)
	}

	// Check that token symbol is included
	if !strings.Contains(html, "USDC") {
		t.Fatalf("expected USDC token symbol in HTML, got: %q", html)
	}

	// Check plain text version too
	if !strings.Contains(text, "What Executed Successfully") {
		t.Fatalf("expected 'What Executed Successfully' section in text, got: %q", text)
	}

	if !strings.Contains(text, "✓ (Simulated)") {
		t.Fatalf("expected '(Simulated)' prefix in text, got: %q", text)
	}
}

func TestBuildBranchAndSkippedSummary_BundlerFailure(t *testing.T) {
	vm := NewVM()
	vm.mu.Lock()
	vm.vars["settings"] = map[string]interface{}{
		"name": "Test Stoploss",
		"uniswapv3_pool": map[string]interface{}{
			"token0": map[string]interface{}{
				"symbol": "USDC",
			},
			"token1": map[string]interface{}{
				"symbol": "USDT",
			},
		},
		"uniswapv3_contracts": map[string]interface{}{
			"swapRouter02": "0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e",
		},
	}
	// Set up TaskNodes to represent a workflow with 7 total steps (1 trigger + 6 nodes)
	// This ensures getTotalWorkflowSteps returns 7
	vm.TaskNodes["balance1"] = &avsproto.TaskNode{Name: "balance1"}
	vm.TaskNodes["eventTrigger"] = &avsproto.TaskNode{Name: "eventTrigger"}
	vm.TaskNodes["get_quote"] = &avsproto.TaskNode{Name: "get_quote"}
	vm.TaskNodes["approve1"] = &avsproto.TaskNode{Name: "approve1"}
	vm.TaskNodes["swap1"] = &avsproto.TaskNode{Name: "swap1"}
	vm.TaskNodes["contractWrite1"] = &avsproto.TaskNode{Name: "contractWrite1"}
	vm.mu.Unlock()

	// Create successful steps before the failure
	// Step 1: Successful balance check (not a contract write, so won't appear in "What Executed Successfully")
	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id:      "balance1",
			Name:    "balance1",
			Success: true,
			Type:    avsproto.NodeType_NODE_TYPE_BALANCE.String(),
		},
		{
			Id:      "eventTrigger",
			Name:    "eventTrigger",
			Success: true,
			Type:    avsproto.NodeType_NODE_TYPE_REST_API.String(), // Using REST_API as a placeholder for trigger
		},
		{
			Id:      "get_quote",
			Name:    "get_quote",
			Success: true,
			Type:    avsproto.NodeType_NODE_TYPE_CONTRACT_READ.String(),
		},
		{
			Id:      "approve1",
			Name:    "approve1",
			Success: false, // Failed step
			Type:    avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(),
			Error:   "Bundler service unavailable: dial tcp [::1]:4437: connect: connection refused",
			Config: func() *structpb.Value {
				configValue, err := structpb.NewValue(map[string]interface{}{
					"contractAddress": "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238",
					"isSimulated":     false,
					"methodCalls": []interface{}{
						map[string]interface{}{
							"methodName": "approve",
						},
					},
				})
				require.NoError(t, err)
				return configValue
			}(),
		},
		// Additional failed step (contractWrite1) - matches screenshot showing 2 failed nodes
		{
			Id:      "contractWrite1",
			Name:    "contractWrite1",
			Success: false,
			Type:    avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(),
			Error:   "ERC20: transfer amount exceeds allowance",
		},
		// Note: swap1 is not included as it may have been skipped or not executed
		// The screenshot shows only 2 failed nodes: approve1 and contractWrite1
	}

	// Call BuildBranchAndSkippedSummary
	text, html := BuildBranchAndSkippedSummary(vm)

	// Calculate expected total from ExecutionLogs (source of truth)
	executedSteps := len(vm.ExecutionLogs)
	// Count skipped nodes (nodes in TaskNodes but not in ExecutionLogs)
	skippedCount := 0
	vm.mu.Lock()
	for nodeID, n := range vm.TaskNodes {
		if n == nil || strings.Contains(nodeID, ".") {
			continue
		}
		found := false
		for _, st := range vm.ExecutionLogs {
			if st.GetName() == n.Name {
				found = true
				break
			}
		}
		if !found {
			skippedCount++
		}
	}
	vm.mu.Unlock()
	expectedTotal := executedSteps
	if skippedCount > 0 {
		expectedTotal = executedSteps + skippedCount
	}

	// Verify the summary line shows failure count
	expectedSummary := fmt.Sprintf("2 out of %d nodes failed during execution", expectedTotal)
	if !strings.Contains(text, expectedSummary) {
		t.Fatalf("expected '%s' in text, got: %q", expectedSummary, text)
	}
	if strings.Contains(text, "All nodes were executed successfully") {
		t.Fatalf("should NOT contain 'All nodes were executed successfully' when there are failures, got: %q", text)
	}

	// Verify HTML also shows failure count
	if !strings.Contains(html, expectedSummary) {
		t.Fatalf("expected '%s' in HTML, got: %q", expectedSummary, html)
	}

	// Verify "What Went Wrong" section appears
	if !strings.Contains(text, "What Went Wrong") {
		t.Fatalf("expected 'What Went Wrong' section in text, got: %q", text)
	}
	if !strings.Contains(html, "What Went Wrong") {
		t.Fatalf("expected 'What Went Wrong' section in HTML, got: %q", html)
	}

	// Verify failed step appears in "What Went Wrong"
	if !strings.Contains(text, "✗ approve1") {
		t.Fatalf("expected failed step 'approve1' in 'What Went Wrong' section, got: %q", text)
	}
	if !strings.Contains(html, "approve1") {
		t.Fatalf("expected failed step 'approve1' in HTML, got: %q", html)
	}

	// Verify error message about bundler is included
	if !strings.Contains(text, "Bundler") || !strings.Contains(text, "unavailable") {
		t.Fatalf("expected bundler error message in text, got: %q", text)
	}
	if !strings.Contains(html, "Bundler") || !strings.Contains(html, "unavailable") {
		t.Fatalf("expected bundler error message in HTML, got: %q", html)
	}

	// Verify "No on-chain transactions executed" message (without "contract writes failed")
	if !strings.Contains(text, "No on-chain transactions executed") {
		t.Fatalf("expected 'No on-chain transactions executed' message, got: %q", text)
	}
	if strings.Contains(text, "contract writes failed") {
		t.Fatalf("should NOT contain 'contract writes failed' message (redundant), got: %q", text)
	}

	// Verify "What Executed Successfully" section is NOT present (no successful contract writes)
	if strings.Contains(text, "What Executed Successfully") {
		t.Fatalf("should NOT have 'What Executed Successfully' section when no successful contract writes, got: %q", text)
	}
	if strings.Contains(html, "What Executed Successfully") {
		t.Fatalf("should NOT have 'What Executed Successfully' section in HTML when no successful contract writes, got: %q", html)
	}
}

func TestBuildBranchAndSkippedSummary_PartialFailureWithSuccessfulWrites(t *testing.T) {
	vm := NewVM()
	vm.mu.Lock()
	vm.vars["settings"] = map[string]interface{}{
		"name": "Test Workflow",
		"uniswapv3_pool": map[string]interface{}{
			"token0": map[string]interface{}{
				"symbol": "USDC",
			},
			"token1": map[string]interface{}{
				"symbol": "USDT",
			},
		},
		"uniswapv3_contracts": map[string]interface{}{
			"swapRouter02": "0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e",
		},
	}
	// Set up TaskNodes to represent a workflow with 3 total steps (1 trigger + 2 nodes)
	vm.TaskNodes["approve1"] = &avsproto.TaskNode{Name: "approve1"}
	vm.TaskNodes["swap1"] = &avsproto.TaskNode{Name: "swap1"}
	vm.mu.Unlock()

	// Create Approval event data for successful approve
	approvalData, err := structpb.NewValue(map[string]interface{}{
		"Approval": map[string]interface{}{
			"owner":   "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
			"spender": "0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e",
			"value":   "1000000", // 1 USDT with 6 decimals
		},
	})
	require.NoError(t, err)

	approveConfig, err := structpb.NewValue(map[string]interface{}{
		"contractAddress": "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238",
		"isSimulated":     false,
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName": "approve",
			},
		},
	})
	require.NoError(t, err)

	// Create swap metadata for failed swap
	swapConfig, err := structpb.NewValue(map[string]interface{}{
		"contractAddress": "0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e",
		"isSimulated":     false,
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName": "exactInputSingle",
			},
		},
	})
	require.NoError(t, err)

	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id:      "approve1",
			Name:    "approve1",
			Success: true, // Successful approve
			Type:    avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(),
			Config:  approveConfig,
			OutputData: &avsproto.Execution_Step_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode_Output{
					Data: approvalData,
				},
			},
		},
		{
			Id:      "swap1",
			Name:    "swap1",
			Success: false, // Failed swap
			Type:    avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(),
			Error:   "Bundler service unavailable: dial tcp [::1]:4437: connect: connection refused",
			Config:  swapConfig,
		},
	}

	// Call BuildBranchAndSkippedSummary
	text, html := BuildBranchAndSkippedSummary(vm)

	// Calculate expected total from ExecutionLogs (source of truth)
	executedSteps := len(vm.ExecutionLogs)
	// Count skipped nodes (nodes in TaskNodes but not in ExecutionLogs)
	skippedCount := 0
	vm.mu.Lock()
	for nodeID, n := range vm.TaskNodes {
		if n == nil || strings.Contains(nodeID, ".") {
			continue
		}
		found := false
		for _, st := range vm.ExecutionLogs {
			if st.GetName() == n.Name {
				found = true
				break
			}
		}
		if !found {
			skippedCount++
		}
	}
	vm.mu.Unlock()
	expectedTotal := executedSteps
	if skippedCount > 0 {
		expectedTotal = executedSteps + skippedCount
	}

	// Verify both sections appear
	if !strings.Contains(text, "What Executed Successfully") {
		t.Fatalf("expected 'What Executed Successfully' section when there are successful writes, got: %q", text)
	}
	if !strings.Contains(text, "What Went Wrong") {
		t.Fatalf("expected 'What Went Wrong' section when there are failures, got: %q", text)
	}

	// Verify successful approve appears in "What Executed Successfully"
	if !strings.Contains(text, "✓ Approved") {
		t.Fatalf("expected successful approve in 'What Executed Successfully', got: %q", text)
	}

	// Verify failed swap appears in "What Went Wrong"
	if !strings.Contains(text, "✗ swap1") {
		t.Fatalf("expected failed swap in 'What Went Wrong', got: %q", text)
	}

	// Verify summary line shows failure count
	expectedSummary := fmt.Sprintf("1 out of %d nodes failed during execution", expectedTotal)
	if !strings.Contains(text, expectedSummary) {
		t.Fatalf("expected '%s' in text, got: %q", expectedSummary, text)
	}

	// Verify on-chain transaction count includes successful writes
	if !strings.Contains(text, "Executed 1 on-chain transaction") {
		t.Fatalf("expected 'Executed 1 on-chain transaction' message, got: %q", text)
	}

	// Verify HTML also has both sections
	if !strings.Contains(html, "What Executed Successfully") {
		t.Fatalf("expected 'What Executed Successfully' section in HTML, got: %q", html)
	}
	if !strings.Contains(html, "What Went Wrong") {
		t.Fatalf("expected 'What Went Wrong' section in HTML, got: %q", html)
	}
}

func TestBuildBranchAndSkippedSummary_WithSkippedNodes(t *testing.T) {
	// Test case matching the scenario from the user's screenshot:
	// - Top message: "executed 4 out of 7 total steps"
	// - Summary: should say "2 out of 7 nodes failed" (not "2 out of 8")
	// - 3 skipped nodes: contractRead1, approve1, contractWrite1
	// - 2 failed nodes: balance1, code1
	vm := NewVM()
	vm.mu.Lock()
	vm.vars["settings"] = map[string]interface{}{
		"name": "Test Stoploss",
	}
	// Set up TaskNodes to include nodes that will be skipped
	// Note: vm.TaskNodes includes nodes from ALL branch paths, but only one path executes
	// For this test: 4 executed nodes (eventTrigger, balance1, code1, branch1) + 3 skipped nodes = 7 total
	vm.TaskNodes["eventTrigger"] = &avsproto.TaskNode{Name: "eventTrigger"}
	vm.TaskNodes["balance1"] = &avsproto.TaskNode{Name: "balance1"}
	vm.TaskNodes["code1"] = &avsproto.TaskNode{Name: "code1"}
	vm.TaskNodes["branch1"] = &avsproto.TaskNode{Name: "branch1"}
	// These nodes are on the skipped branch path (3 skipped nodes)
	vm.TaskNodes["contractRead1"] = &avsproto.TaskNode{Name: "contractRead1"}
	vm.TaskNodes["approve1"] = &avsproto.TaskNode{Name: "approve1"}
	vm.TaskNodes["contractWrite1"] = &avsproto.TaskNode{Name: "contractWrite1"}
	// Note: email1 is not in TaskNodes because it's not part of the workflow definition
	// in this scenario (it might be executed but not tracked as a workflow node)
	vm.mu.Unlock()

	// Create execution logs for executed steps
	// Based on the scenario: 4 executed steps (eventTrigger, balance1, code1, branch1)
	// Note: email1 is executed but might not be counted in the "executed steps" for the top message
	// For this test, we'll match the scenario: 4 executed + 3 skipped = 7 total
	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id:      "eventTrigger",
			Name:    "eventTrigger",
			Success: true,
			Type:    "eventTrigger",
		},
		{
			Id:      "balance1",
			Name:    "balance1",
			Success: false,
			Type:    avsproto.NodeType_NODE_TYPE_BALANCE.String(),
			Error:   "failed to fetch balances: moralis API returned status 502: error code: 500",
		},
		{
			Id:      "code1",
			Name:    "code1",
			Success: false,
			Type:    avsproto.NodeType_NODE_TYPE_CUSTOM_CODE.String(),
			Error:   "failed to execute script: TypeError: Cannot read property 'find' of undefined or null at <eval>:4:31(3)",
		},
		{
			Id:      "branch1",
			Name:    "branch1",
			Success: true,
			Type:    avsproto.NodeType_NODE_TYPE_BRANCH.String(),
		},
		// Note: email1 is not included in ExecutionLogs to match the scenario
		// where "executed 4 out of 7 total steps" (4 executed + 3 skipped = 7)
	}

	// Call BuildBranchAndSkippedSummary
	text, html := BuildBranchAndSkippedSummary(vm)

	// Verify the summary line shows correct count: 2 out of 7 nodes failed
	// (4 executed + 3 skipped = 7 total, not 8)
	if !strings.Contains(text, "2 out of 7 nodes failed during execution") {
		t.Fatalf("expected '2 out of 7 nodes failed during execution' in text, got: %q", text)
	}
	if strings.Contains(text, "2 out of 8 nodes failed") {
		t.Fatalf("should NOT contain '2 out of 8 nodes failed' (should be 7 total), got: %q", text)
	}

	// Verify HTML also shows correct count
	if !strings.Contains(html, "2 out of 7 nodes failed during execution") {
		t.Fatalf("expected '2 out of 7 nodes failed during execution' in HTML, got: %q", html)
	}
	if strings.Contains(html, "2 out of 8 nodes failed") {
		t.Fatalf("should NOT contain '2 out of 8 nodes failed' in HTML (should be 7 total), got: %q", html)
	}

	// Verify "What Went Wrong" section appears
	if !strings.Contains(text, "What Went Wrong") {
		t.Fatalf("expected 'What Went Wrong' section in text, got: %q", text)
	}

	// Verify failed steps appear in "What Went Wrong"
	if !strings.Contains(text, "✗ balance1") {
		t.Fatalf("expected failed step 'balance1' in 'What Went Wrong' section, got: %q", text)
	}
	if !strings.Contains(text, "✗ code1") {
		t.Fatalf("expected failed step 'code1' in 'What Went Wrong' section, got: %q", text)
	}

	// Verify skipped nodes section appears
	if !strings.Contains(text, "The below nodes were skipped due to branching conditions") {
		t.Fatalf("expected skipped nodes section in text, got: %q", text)
	}

	// Verify skipped nodes are listed
	if !strings.Contains(text, "- contractRead1") {
		t.Fatalf("expected skipped node 'contractRead1' in text, got: %q", text)
	}
	if !strings.Contains(text, "- approve1") {
		t.Fatalf("expected skipped node 'approve1' in text, got: %q", text)
	}
	if !strings.Contains(text, "- contractWrite1") {
		t.Fatalf("expected skipped node 'contractWrite1' in text, got: %q", text)
	}

	// Verify "No on-chain transactions executed" message
	if !strings.Contains(text, "No on-chain transactions executed") {
		t.Fatalf("expected 'No on-chain transactions executed' message, got: %q", text)
	}
}

// TestBuildStepsOverview_ApproveWithLowercaseKey tests the fix for extracting spender
// from approve outputData when it uses lowercase "approve" key instead of "Approval" event key.
// This test uses the real response structure from the terminal output.
func TestBuildStepsOverview_ApproveWithLowercaseKey(t *testing.T) {
	vm := NewVM()
	vm.mu.Lock()
	vm.vars["settings"] = map[string]interface{}{
		"uniswapv3_pool": map[string]interface{}{
			"tokens": map[string]interface{}{
				"input": "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", // USDC
			},
			"token1": map[string]interface{}{
				"symbol": "USDC",
			},
		},
		"uniswapv3_contracts": map[string]interface{}{
			"swapRouter02": "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E",
		},
	}
	vm.mu.Unlock()

	// Create approve outputData with lowercase "approve" key (matching real response structure)
	// This is the actual structure from the terminal output:
	// "outputData": {
	//   "approve": {
	//     "owner": "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
	//     "spender": "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E",
	//     "value": "20990000"
	//   }
	// }
	approveOutputData, err := structpb.NewValue(map[string]interface{}{
		"approve": map[string]interface{}{
			"owner":   "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
			"spender": "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E",
			"value":   "20990000", // 20.99 USDC (6 decimals)
		},
	})
	require.NoError(t, err)

	configValue, err := structpb.NewValue(map[string]interface{}{
		"contractAddress": "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", // USDC token address
		"isSimulated":     false,
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName": "approve",
			},
		},
	})
	require.NoError(t, err)

	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id:      "approve1",
			Name:    "approve1",
			Success: true,
			Type:    avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(),
			Config:  configValue,
			OutputData: &avsproto.Execution_Step_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode_Output{
					Data: approveOutputData,
				},
			},
		},
	}

	result := buildStepsOverview(vm)

	// Verify that spender is correctly extracted and included in the description
	// Before the fix, this would show "Approved token to " (missing spender)
	// After the fix, it should show the spender address (shortened format)

	// Verify that the description is complete (not truncated)
	// Should NOT end with "Approved token to " (missing spender)
	if strings.HasSuffix(strings.TrimSpace(result), "Approved token to") {
		t.Fatalf("description should include spender address, got incomplete description: %q", result)
	}

	// Verify that the spender address appears in shortened format
	// The shortHexAddr function formats addresses as "0x...last4chars"
	// Check for either the shortened format or the contract name + shortened format
	if !strings.Contains(result, "0x3bFA") && !strings.Contains(result, "0x3bfa") {
		// If not found, check if it's using the contract name instead
		if !strings.Contains(result, "Uniswap V3 router") {
			t.Fatalf("expected spender address (shortened) or contract name in description, got: %q", result)
		}
	}

	// Verify that the description contains "to" followed by something (the spender)
	// This ensures the spender is present
	if !strings.Contains(result, " to ") {
		t.Fatalf("expected ' to ' in description (indicating spender is present), got: %q", result)
	}

	// Verify that value is included (20.99 USDC)
	// The value "20990000" with 6 decimals should format to "20.99"
	if !strings.Contains(result, "20.99") && !strings.Contains(result, "20.9900") {
		t.Fatalf("expected formatted amount (20.99) in description, got: %q", result)
	}

	// Verify that token symbol is included if available
	if strings.Contains(result, "USDC") {
		// If USDC is present, verify the full description format
		if !strings.Contains(result, "Approved") {
			t.Fatalf("expected 'Approved' in description, got: %q", result)
		}
	}

	// Verify checkmark prefix
	if !strings.Contains(result, "✓") {
		t.Fatalf("expected checkmark prefix, got: %q", result)
	}

	// Verify the description is not empty or just whitespace
	if strings.TrimSpace(result) == "" {
		t.Fatalf("expected non-empty description, got empty string")
	}

	// Log the result for debugging
	t.Logf("Generated description: %q", result)

	// Verify the exact format matches expected output
	// Should be: "✓ Approved 20.9900 USDC to Uniswap V3 router 0x3bFA...e48E"
	expectedParts := []string{
		"✓ Approved",
		"20.9900",
		"USDC",
		"to",
		"Uniswap V3 router",
		"0x3bFA",
	}
	for _, part := range expectedParts {
		if !strings.Contains(result, part) {
			t.Fatalf("expected description to contain %q, got: %q", part, result)
		}
	}
}

// TestBuildStepsOverview_ApproveWithBothKeys verifies that "Approval" event takes priority
// over "approve" output data when both are present.
func TestBuildStepsOverview_ApproveWithBothKeys(t *testing.T) {
	vm := NewVM()
	vm.mu.Lock()
	vm.vars["settings"] = map[string]interface{}{
		"uniswapv3_pool": map[string]interface{}{
			"token1": map[string]interface{}{
				"symbol": "USDT",
			},
		},
		"uniswapv3_contracts": map[string]interface{}{
			"swapRouter02": "0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e",
		},
	}
	vm.mu.Unlock()

	// Create outputData with BOTH "Approval" event and "approve" output data
	// The "Approval" event should take priority
	approveOutputData, err := structpb.NewValue(map[string]interface{}{
		"Approval": map[string]interface{}{
			"owner":   "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
			"spender": "0x3bfa4769fb09eefc5a80d6e87c3b9c650f7ae48e", // From Approval event (should be used)
			"value":   "1000000",                                    // 1 USDT
		},
		"approve": map[string]interface{}{
			"owner":   "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
			"spender": "0x0000000000000000000000000000000000000000", // Different spender (should be ignored)
			"value":   "999999",                                     // Different value (should be ignored)
		},
	})
	require.NoError(t, err)

	configValue, err := structpb.NewValue(map[string]interface{}{
		"contractAddress": "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238",
		"isSimulated":     false,
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName": "approve",
			},
		},
	})
	require.NoError(t, err)

	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id:      "approve1",
			Name:    "approve1",
			Success: true,
			Type:    avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(),
			Config:  configValue,
			OutputData: &avsproto.Execution_Step_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode_Output{
					Data: approveOutputData,
				},
			},
		},
	}

	result := buildStepsOverview(vm)

	// Verify that the "Approval" event data is used (not the "approve" output data)
	// Should contain "1.0" or "1.0000" (from Approval event value: 1000000)
	if !strings.Contains(result, "1.0") && !strings.Contains(result, "1.0000") {
		t.Fatalf("expected value from Approval event (1.0), got: %q", result)
	}

	// Should NOT contain the value from "approve" output data (999999)
	if strings.Contains(result, "999999") {
		t.Fatalf("should use Approval event value, not approve output value, got: %q", result)
	}

	// Should contain the spender from Approval event (Uniswap V3 router)
	if !strings.Contains(result, "Uniswap V3 router") {
		t.Fatalf("expected spender from Approval event, got: %q", result)
	}

	// Should NOT contain the zero address from "approve" output data
	if strings.Contains(result, "0x0000") || strings.Contains(result, "0x0") {
		t.Fatalf("should use Approval event spender, not approve output spender, got: %q", result)
	}

	t.Logf("Generated description (both keys present, Approval takes priority): %q", result)
}
