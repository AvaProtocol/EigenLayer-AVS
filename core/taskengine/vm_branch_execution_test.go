package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestRunTaskWithBranchNode(t *testing.T) {
	nodes := []*avsproto.TaskNode{
		{
			Id:   "123",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Config: &avsproto.RestAPINode_Config{
						Url:    "https://mock-api.ap-aggregator.local/get",
						Method: "GET",
					},
				},
			},
		},
		{
			Id:   "456",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Config: &avsproto.BranchNode_Config{
						Conditions: []*avsproto.BranchNode_Condition{
							{
								Id:         "condition1",
								Type:       "if",
								Expression: "httpnode.data.url == 'https://mock-api.ap-aggregator.local/get'",
							},
						},
					},
				},
			},
		},
		{
			Id:   "789",
			Name: "httpnode2",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Config: &avsproto.RestAPINode_Config{
						Url:    "https://mock-api.ap-aggregator.local/get",
						Method: "GET",
					},
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "123",
		},
		{
			Id:     "e2",
			Source: "123",
			Target: "456",
		},
		{
			Id:     "e3",
			Source: "456.condition1",
			Target: "789",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Fatalf("expect vm initialized, got error: %v", err)
	}

	vm.Compile()

	if vm.entrypoint != "triggertest" {
		t.Errorf("Error compute entrypoint. Expected triggertest Got %s", vm.entrypoint)
	}
	err = vm.Run()
	if err != nil {
		t.Errorf("Error executing program. Expected no error Got error %v", err)
	}

	vm.mu.Lock()
	tempData, exists := vm.vars["httpnode"]
	vm.mu.Unlock()

	if !exists {
		t.Errorf("httpnode variable not found in VM vars")
		return
	}

	if tempData == nil {
		t.Errorf("httpnode variable is nil")
		return
	}

	vm.mu.Lock()
	tempData2, exists2 := vm.vars["httpnode2"]
	vm.mu.Unlock()

	if !exists2 {
		t.Errorf("httpnode2 variable not found in VM vars")
		return
	}

	if tempData2 == nil {
		t.Errorf("httpnode2 variable is nil")
		return
	}

	vm.mu.Lock()
	branchData, branchExists := vm.vars["branch"]
	vm.mu.Unlock()

	if !branchExists {
		t.Errorf("branch variable not found in VM vars")
		return
	}

	if branchData == nil {
		t.Errorf("branch variable is nil")
		return
	}

	branchMap, ok := branchData.(map[string]any)
	if !ok {
		t.Errorf("branch is not map[string]any, got %T: %v", branchData, branchData)
		return
	}

	data, dataExists := branchMap["data"]
	if !dataExists {
		t.Errorf("data field not found in branch response, available fields: %v", branchMap)
		return
	}

	dataMap, ok := data.(map[string]any)
	if !ok {
		t.Errorf("data is not map[string]any, got %T: %v", data, data)
		return
	}

	conditionResults, conditionExists := dataMap["condition_results"]
	if !conditionExists {
		t.Errorf("condition_results field not found in data, available fields: %v", dataMap)
		return
	}

	conditionResultsSlice, ok := conditionResults.([]map[string]interface{})
	if !ok {
		t.Errorf("condition_results is not []map[string]interface{}, got %T: %v", conditionResults, conditionResults)
		return
	}

	if len(conditionResultsSlice) != 1 {
		t.Errorf("Expected 1 condition result, got %d", len(conditionResultsSlice))
		return
	}

	firstCondition := conditionResultsSlice[0]

	if firstCondition["id"] != "condition1" {
		t.Errorf("Expected condition id 'condition1', got %v", firstCondition["id"])
	}

	if firstCondition["result"] != true {
		t.Errorf("Expected condition result true, got %v", firstCondition["result"])
	}

	if firstCondition["next_node_id"] != "789" {
		t.Errorf("Expected next_node_id '789', got %v", firstCondition["next_node_id"])
	}
}

func TestBranchNode_FindExpression(t *testing.T) {
	// This test reproduces the client request with .find() expression
	// Using the EXACT input variables from the client's example

	// Set up balance data - exact structure from client
	balance1Data := map[string]interface{}{
		"data": []interface{}{
			map[string]interface{}{
				"balance":          "220001808234796117",
				"balanceFormatted": "0.220001808234796117",
				"decimals":         float64(18),
				"name":             "Ether",
				"symbol":           "ETH",
			},
			map[string]interface{}{
				"balance":          "1000000000000000",
				"balanceFormatted": "0.001",
				"decimals":         float64(18),
				"name":             "Wrapped Ether",
				"symbol":           "WETH",
				"tokenAddress":     "0xfff9976782d46cc05630d1f6ebab18b2324d6b14",
			},
			map[string]interface{}{
				"balance":          "535124566",
				"balanceFormatted": "535.124566",
				"decimals":         float64(6),
				"name":             "USDC",
				"symbol":           "USDC",
				"tokenAddress":     "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238", // lowercase
			},
		},
	}

	// Set up settings data - exact structure from client
	settingsData := map[string]interface{}{
		"chain":    "Sepolia",
		"amount":   "10",
		"runner":   "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e",
		"chain_id": float64(11155111),
		"uniswap_v3_pool": map[string]interface{}{
			"id": "0xee8027d8430344ba3419f844ba858ac7f1a92095",
			"token0": map[string]interface{}{
				"id":     "0xfff9976782d46cc05630d1f6ebab18b2324d6b14",
				"symbol": "WETH",
			},
			"token1": map[string]interface{}{
				"id":     "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", // mixed case (checksummed)
				"symbol": "USDC",
			},
			"feeTier": "3000",
		},
		"uniswap_v3_contracts": map[string]interface{}{
			"quoterV2":     "0xEd1f6473345F45b75F8179591dd5bA1888cf2FB3",
			"swapRouter02": "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E",
		},
	}

	// Create branch conditions
	conditions := []*avsproto.BranchNode_Condition{
		{
			Id:   "0",
			Type: "if",
			// Updated expression with > 0 comparison (as client fixed)
			// But still fails due to case-sensitive address comparison
			Expression: "{{balance1.data.find(token => token.tokenAddress === settings.uniswap_v3_pool.token1.id).balance > 0}}",
		},
		{
			Id:         "1",
			Type:       "else",
			Expression: "",
		},
	}

	branchNode := &avsproto.BranchNode{
		Config: &avsproto.BranchNode_Config{
			Conditions: conditions,
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch-node-1",
			Name: "branchNode",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: branchNode,
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "trigger-1",
		Name: "trigger-1",
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "branch-node-1",
		},
	}

	// Create VM
	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "test-task",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Fatalf("failed to create VM: %v", err)
	}

	// Add variables to VM
	vm.AddVar("balance1", balance1Data)
	vm.AddVar("settings", settingsData)
	vm.AddVar("eventTrigger", map[string]interface{}{"data": map[string]interface{}{}})

	// Execute branch node
	processor := NewBranchProcessor(vm)
	step, nextStep, err := processor.Execute("branch-node-1", branchNode)

	t.Logf("Execution log:\n%s", step.Log)

	if err != nil {
		t.Fatalf("branch execution failed: %v", err)
	}

	if !step.Success {
		t.Fatalf("expected branch to succeed but got error: %s", step.Error)
	}

	// Get the branch output
	branchOutput := step.GetBranch()
	if branchOutput == nil {
		t.Fatal("expected branch output but got nil")
	}

	outputData := branchOutput.Data.AsInterface().(map[string]interface{})
	conditionId := outputData["conditionId"].(string)

	t.Logf("Branch took condition: %s", conditionId)

	// The client's data has case-sensitivity issue:
	// - balance1 tokenAddress: "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238" (lowercase)
	// - settings token1.id:     "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238" (checksummed)
	// The === comparison is case-sensitive, so .find() returns undefined
	if conditionId != "branch-node-1.1" {
		t.Errorf("❌ Expected branch to take condition 1 (else) because .find() returns undefined due to case mismatch, but took: %s", conditionId)
	} else {
		t.Log("✅ Branch correctly took the else condition")
		t.Log("   Issue: .find() returns undefined because addresses have different casing:")
		t.Log("   - balance tokenAddress: 0x1c7d4b196... (lowercase)")
		t.Log("   - settings token1.id:   0x1c7D4B196... (checksummed)")
		t.Log("   Fix: Use case-insensitive comparison with null check:")
		t.Log("   {{balance1.data.find(token => token.tokenAddress && token.tokenAddress.toLowerCase() === settings.uniswap_v3_pool.token1.id.toLowerCase()).balance > 0}}")
	}

	if nextStep != nil {
		t.Logf("Next step: %s", nextStep.NodeID)
	}
}

func TestBranchNode_FindExpression_CaseInsensitive(t *testing.T) {
	// This test shows the CORRECT way to handle the client's request
	// Using case-insensitive address comparison

	// Set up balance data - exact structure from client
	balance1Data := map[string]interface{}{
		"data": []interface{}{
			map[string]interface{}{
				"balance":          "220001808234796117",
				"balanceFormatted": "0.220001808234796117",
				"decimals":         float64(18),
				"name":             "Ether",
				"symbol":           "ETH",
			},
			map[string]interface{}{
				"balance":          "1000000000000000",
				"balanceFormatted": "0.001",
				"decimals":         float64(18),
				"name":             "Wrapped Ether",
				"symbol":           "WETH",
				"tokenAddress":     "0xfff9976782d46cc05630d1f6ebab18b2324d6b14",
			},
			map[string]interface{}{
				"balance":          "535124566",
				"balanceFormatted": "535.124566",
				"decimals":         float64(6),
				"name":             "USDC",
				"symbol":           "USDC",
				"tokenAddress":     "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238", // lowercase
			},
		},
	}

	// Set up settings data - exact structure from client
	settingsData := map[string]interface{}{
		"chain":    "Sepolia",
		"amount":   "10",
		"runner":   "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e",
		"chain_id": float64(11155111),
		"uniswap_v3_pool": map[string]interface{}{
			"id": "0xee8027d8430344ba3419f844ba858ac7f1a92095",
			"token0": map[string]interface{}{
				"id":     "0xfff9976782d46cc05630d1f6ebab18b2324d6b14",
				"symbol": "WETH",
			},
			"token1": map[string]interface{}{
				"id":     "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", // mixed case (checksummed)
				"symbol": "USDC",
			},
			"feeTier": "3000",
		},
		"uniswap_v3_contracts": map[string]interface{}{
			"quoterV2":     "0xEd1f6473345F45b75F8179591dd5bA1888cf2FB3",
			"swapRouter02": "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E",
		},
	}

	// Create branch conditions
	conditions := []*avsproto.BranchNode_Condition{
		{
			Id:   "0",
			Type: "if",
			// CORRECT expression with case-insensitive comparison
			// Need to check if tokenAddress exists first (ETH doesn't have tokenAddress)
			Expression: "{{balance1.data.find(token => token.tokenAddress && token.tokenAddress.toLowerCase() === settings.uniswap_v3_pool.token1.id.toLowerCase()).balance > 0}}",
		},
		{
			Id:         "1",
			Type:       "else",
			Expression: "",
		},
	}

	branchNode := &avsproto.BranchNode{
		Config: &avsproto.BranchNode_Config{
			Conditions: conditions,
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch-node-1",
			Name: "branchNode",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: branchNode,
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "trigger-1",
		Name: "trigger-1",
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "branch-node-1",
		},
	}

	// Create VM
	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "test-task",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Fatalf("failed to create VM: %v", err)
	}

	// Add variables to VM
	vm.AddVar("balance1", balance1Data)
	vm.AddVar("settings", settingsData)
	vm.AddVar("eventTrigger", map[string]interface{}{"data": map[string]interface{}{}})

	// Execute branch node
	processor := NewBranchProcessor(vm)
	step, nextStep, err := processor.Execute("branch-node-1", branchNode)

	t.Logf("Execution log:\n%s", step.Log)

	if err != nil {
		t.Fatalf("branch execution failed: %v", err)
	}

	if !step.Success {
		t.Fatalf("expected branch to succeed but got error: %s", step.Error)
	}

	// Get the branch output
	branchOutput := step.GetBranch()
	if branchOutput == nil {
		t.Fatal("expected branch output but got nil")
	}

	outputData := branchOutput.Data.AsInterface().(map[string]interface{})
	conditionId := outputData["conditionId"].(string)

	t.Logf("Branch took condition: %s", conditionId)

	// With case-insensitive comparison, it should find USDC and take the if condition
	if conditionId != "branch-node-1.0" {
		t.Errorf("❌ Expected branch to take condition 0 (if) with case-insensitive comparison, but took: %s", conditionId)
	} else {
		t.Log("✅ Branch correctly took the if condition")
		t.Log("   With .toLowerCase() on both sides, the addresses match correctly")
		t.Log("   Expression evaluated to: true (USDC balance 535124566 > 0)")
	}

	if nextStep != nil {
		t.Logf("Next step: %s", nextStep.NodeID)
	}
}

func TestBranchNode_NumericComparison(t *testing.T) {
	// Test numeric comparison with string values
	// Client wants to check if USDC balance > settings.amount

	balance1Data := map[string]interface{}{
		"data": []interface{}{
			map[string]interface{}{
				"balance":          "220001808234796117",
				"balanceFormatted": "0.220001808234796117",
				"decimals":         float64(18),
				"name":             "Ether",
				"symbol":           "ETH",
			},
			map[string]interface{}{
				"balance":          "1000000000000000",
				"balanceFormatted": "0.001",
				"decimals":         float64(18),
				"name":             "Wrapped Ether",
				"symbol":           "WETH",
				"tokenAddress":     "0xfff9976782d46cc05630d1f6ebab18b2324d6b14",
			},
			map[string]interface{}{
				"balance":          "535124566",
				"balanceFormatted": "535.124566",
				"decimals":         float64(6),
				"name":             "USDC",
				"symbol":           "USDC",
				"tokenAddress":     "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238",
			},
		},
	}

	settingsData := map[string]interface{}{
		"chain":    "Sepolia",
		"amount":   "10", // String value
		"runner":   "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e",
		"chain_id": float64(11155111),
		"uniswap_v3_pool": map[string]interface{}{
			"id": "0xee8027d8430344ba3419f844ba858ac7f1a92095",
			"token0": map[string]interface{}{
				"id":     "0xfff9976782d46cc05630d1f6ebab18b2324d6b14",
				"symbol": "WETH",
			},
			"token1": map[string]interface{}{
				"id":     "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
				"symbol": "USDC",
			},
			"feeTier": "3000",
		},
		"uniswap_v3_contracts": map[string]interface{}{
			"quoterV2":     "0xEd1f6473345F45b75F8179591dd5bA1888cf2FB3",
			"swapRouter02": "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E",
		},
	}

	conditions := []*avsproto.BranchNode_Condition{
		{
			Id:   "0",
			Type: "if",
			// Correct expression: use balanceFormatted and convert both to numbers
			Expression: "{{Number(balance1.data.find(token => token.tokenAddress && token.tokenAddress.toLowerCase() === settings.uniswap_v3_pool.token1.id.toLowerCase()).balanceFormatted) > Number(settings.amount)}}",
		},
		{
			Id:         "1",
			Type:       "else",
			Expression: "",
		},
	}

	branchNode := &avsproto.BranchNode{
		Config: &avsproto.BranchNode_Config{
			Conditions: conditions,
		},
	}

	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch-node-1",
			Name: "branchNode",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: branchNode,
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "trigger-1",
		Name: "trigger-1",
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "e1",
			Source: trigger.Id,
			Target: "branch-node-1",
		},
	}

	vm, err := NewVMWithData(&model.Task{
		Task: &avsproto.Task{
			Id:      "test-task",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)

	if err != nil {
		t.Fatalf("failed to create VM: %v", err)
	}

	vm.AddVar("balance1", balance1Data)
	vm.AddVar("settings", settingsData)
	vm.AddVar("eventTrigger", map[string]interface{}{"data": map[string]interface{}{}})

	processor := NewBranchProcessor(vm)
	step, nextStep, err := processor.Execute("branch-node-1", branchNode)

	t.Logf("Execution log:\n%s", step.Log)

	if err != nil {
		t.Fatalf("branch execution failed: %v", err)
	}

	if !step.Success {
		t.Fatalf("expected branch to succeed but got error: %s", step.Error)
	}

	branchOutput := step.GetBranch()
	if branchOutput == nil {
		t.Fatal("expected branch output but got nil")
	}

	outputData := branchOutput.Data.AsInterface().(map[string]interface{})
	conditionId := outputData["conditionId"].(string)

	t.Logf("Branch took condition: %s", conditionId)

	// USDC balance is 535.124566, which is > 10, so should take if condition
	if conditionId != "branch-node-1.0" {
		t.Errorf("❌ Expected branch to take condition 0 (if) because 535.124566 > 10, but took: %s", conditionId)
	} else {
		t.Log("✅ Branch correctly took the if condition")
		t.Log("   Comparison: 535.124566 (USDC balance) > 10 (settings.amount)")
		t.Log("   Used Number() conversion to handle string-to-number comparison")
	}

	if nextStep != nil {
		t.Logf("Next step: %s", nextStep.NodeID)
	}
}
