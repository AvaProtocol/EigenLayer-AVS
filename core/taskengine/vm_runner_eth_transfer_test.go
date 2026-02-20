package taskengine

import (
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestETHTransferProcessor_Execute_Success(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	testUserAddress := testutil.TestUser1().Address
	processor := NewETHTransferProcessor(vm, nil, nil, &testUserAddress)

	node := &avsproto.ETHTransferNode{
		Config: &avsproto.ETHTransferNode_Config{
			Destination: "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6",
			Amount:      "1000000000000000000", // 1 ETH in wei
		},
	}

	// Set up TaskNode for proper variable mapping
	taskNode := &avsproto.TaskNode{
		Id:   "test-step",
		Name: "TestETHTransfer",
		Type: avsproto.NodeType_NODE_TYPE_ETH_TRANSFER,
		TaskType: &avsproto.TaskNode_EthTransfer{
			EthTransfer: node,
		},
	}
	processor.CommonProcessor.SetTaskNode(taskNode)

	// Also add to VM's TaskNodes map for getNodeNameAsVarLocked to work
	vm.TaskNodes = map[string]*avsproto.TaskNode{
		"test-step": taskNode,
	}

	executionLog, err := processor.Execute("test-step", node)

	if err != nil {
		t.Errorf("Expected no error, got: %v", err)
	}

	if executionLog == nil {
		t.Fatal("Expected execution log, got nil")
	}

	if !executionLog.Success {
		t.Errorf("Expected success=true, got success=%v, error=%s", executionLog.Success, executionLog.Error)
	}

	if executionLog.Id != "test-step" {
		t.Errorf("Expected Id='test-step', got '%s'", executionLog.Id)
	}

	// Check output data
	ethTransferOutput := executionLog.GetEthTransfer()
	if ethTransferOutput == nil {
		t.Fatal("Expected ETH transfer output data, got nil")
	}

	if ethTransferOutput.Data == nil {
		t.Error("Expected ETH transfer data to be set")
	}

	// Check that output variables were set
	// Note: GetOutputVar extracts nodeVar["data"], so we get the transfer object directly
	outputVar := processor.GetOutputVar("test-step")
	if outputVar == nil {
		t.Fatal("Expected output variable to be set")
	}

	outputMap, ok := outputVar.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected output variable to be a map, got %T", outputVar)
	}

	// Check the transfer object that matches ERC20 transfer format
	// GetOutputVar returns nodeVar["data"] directly, so transfer is at the top level
	transferField, hasTransfer := outputMap["transfer"]
	if !hasTransfer {
		t.Errorf("Expected 'transfer' field in output, got: %+v", outputMap)
	} else {
		transferMap, ok := transferField.(map[string]interface{})
		if !ok {
			t.Error("Expected 'transfer' field to be a map")
		} else {
			if transferMap["to"] != "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6" {
				t.Errorf("Expected 'transfer.to' field, got: %v", transferMap["to"])
			}
			if transferMap["value"] != "1000000000000000000" {
				t.Errorf("Expected 'transfer.value' field, got: %v", transferMap["value"])
			}
		}
	}
}

func TestETHTransferProcessor_Execute_InvalidDestination(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	testUserAddress := testutil.TestUser1().Address
	processor := NewETHTransferProcessor(vm, nil, nil, &testUserAddress)

	node := &avsproto.ETHTransferNode{
		Config: &avsproto.ETHTransferNode_Config{
			Destination: "invalid-address",
			Amount:      "1000000000000000000",
		},
	}

	executionLog, err := processor.Execute("test-step", node)

	if err == nil {
		t.Error("Expected error for invalid destination address")
	}

	if executionLog == nil {
		t.Fatal("Expected execution log even on error")
	}

	if executionLog.Success {
		t.Error("Expected success=false for invalid destination")
	}

	if executionLog.Error == "" {
		t.Error("Expected error message to be set")
	}
}

func TestETHTransferProcessor_Execute_InvalidAmount(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	testUserAddress := testutil.TestUser1().Address
	processor := NewETHTransferProcessor(vm, nil, nil, &testUserAddress)

	node := &avsproto.ETHTransferNode{
		Config: &avsproto.ETHTransferNode_Config{
			Destination: "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6",
			Amount:      "invalid-amount",
		},
	}

	executionLog, err := processor.Execute("test-step", node)

	if err == nil {
		t.Error("Expected error for invalid amount")
	}

	if executionLog == nil {
		t.Fatal("Expected execution log even on error")
	}

	if executionLog.Success {
		t.Error("Expected success=false for invalid amount")
	}

	if executionLog.Error == "" {
		t.Error("Expected error message to be set")
	}
}

func TestETHTransferProcessor_Execute_MissingConfig(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	testUserAddress := testutil.TestUser1().Address
	processor := NewETHTransferProcessor(vm, nil, nil, &testUserAddress)

	node := &avsproto.ETHTransferNode{
		Config: nil,
	}

	executionLog, err := processor.Execute("test-step", node)

	if err == nil {
		t.Error("Expected error for missing config")
	}

	if executionLog == nil {
		t.Fatal("Expected execution log even on error")
	}

	if executionLog.Success {
		t.Error("Expected success=false for missing config")
	}

	if executionLog.Error == "" {
		t.Error("Expected error message to be set")
	}
}

func TestETHTransferProcessor_Execute_EmptyDestination(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	testUserAddress := testutil.TestUser1().Address
	processor := NewETHTransferProcessor(vm, nil, nil, &testUserAddress)

	node := &avsproto.ETHTransferNode{
		Config: &avsproto.ETHTransferNode_Config{
			Destination: "",
			Amount:      "1000000000000000000",
		},
	}

	executionLog, err := processor.Execute("test-step", node)

	if err == nil {
		t.Error("Expected error for empty destination")
	}

	if executionLog == nil {
		t.Fatal("Expected execution log even on error")
	}

	if executionLog.Success {
		t.Error("Expected success=false for empty destination")
	}

	if executionLog.Error == "" {
		t.Error("Expected error message to be set")
	}
}

func TestETHTransferProcessor_Execute_EmptyAmount(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	testUserAddress := testutil.TestUser1().Address
	processor := NewETHTransferProcessor(vm, nil, nil, &testUserAddress)

	node := &avsproto.ETHTransferNode{
		Config: &avsproto.ETHTransferNode_Config{
			Destination: "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6",
			Amount:      "",
		},
	}

	executionLog, err := processor.Execute("test-step", node)

	if err == nil {
		t.Error("Expected error for empty amount")
	}

	if executionLog == nil {
		t.Fatal("Expected execution log even on error")
	}

	if executionLog.Success {
		t.Error("Expected success=false for empty amount")
	}

	if executionLog.Error == "" {
		t.Error("Expected error message to be set")
	}
}

func TestETHTransferProcessor_Execute_MaxAmountNoEthClient(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	testUserAddress := testutil.TestUser1().Address
	// Pass nil ethClient — MAX resolution should fail with clear error
	processor := NewETHTransferProcessor(vm, nil, nil, &testUserAddress)

	// Set aa_sender so we get past the first check
	vm.AddVar("aa_sender", "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6")

	node := &avsproto.ETHTransferNode{
		Config: &avsproto.ETHTransferNode_Config{
			Destination: "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6",
			Amount:      "MAX",
		},
	}

	executionLog, err := processor.Execute("test-step", node)

	if err == nil {
		t.Fatal("Expected error for MAX amount with nil ethClient")
	}

	if !strings.Contains(err.Error(), "no RPC client available") {
		t.Errorf("Expected 'no RPC client available' error, got: %v", err)
	}

	if executionLog.Success {
		t.Error("Expected success=false")
	}

	// Verify log contains the resolved values and error
	if !strings.Contains(executionLog.Log, "Resolved: destination=") {
		t.Error("Expected log to contain resolved config values")
	}
	if !strings.Contains(executionLog.Log, "amount=MAX") {
		t.Error("Expected log to show amount=MAX before resolution")
	}
	if !strings.Contains(executionLog.Log, "Error:") {
		t.Error("Expected log to contain error details")
	}
}

func TestETHTransferProcessor_Execute_MaxAmountNoAASender(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	testUserAddress := testutil.TestUser1().Address
	processor := NewETHTransferProcessor(vm, nil, nil, &testUserAddress)

	// Do NOT set aa_sender — MAX resolution should fail
	node := &avsproto.ETHTransferNode{
		Config: &avsproto.ETHTransferNode_Config{
			Destination: "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6",
			Amount:      "MAX",
		},
	}

	executionLog, err := processor.Execute("test-step", node)

	if err == nil {
		t.Fatal("Expected error for MAX amount without aa_sender")
	}

	if !strings.Contains(err.Error(), "smart wallet address not available") {
		t.Errorf("Expected 'smart wallet address not available' error, got: %v", err)
	}

	if executionLog.Success {
		t.Error("Expected success=false")
	}
}

func TestETHTransferProcessor_Execute_MaxAmountCaseInsensitive(t *testing.T) {
	testUserAddress := testutil.TestUser1().Address

	// Test all case variants of "max"
	for _, amount := range []string{"max", "Max", "MAX"} {
		// Use fresh VM and processor per iteration to avoid state leakage
		vm := NewVM()
		vm.WithLogger(testutil.GetLogger())
		vm.AddVar("aa_sender", "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6")

		processor := NewETHTransferProcessor(vm, nil, nil, &testUserAddress)

		node := &avsproto.ETHTransferNode{
			Config: &avsproto.ETHTransferNode_Config{
				Destination: "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6",
				Amount:      amount,
			},
		}

		_, err := processor.Execute("test-step", node)

		if err == nil {
			t.Fatalf("Expected error for amount=%q with nil ethClient", amount)
		}

		// All variants should reach the ethClient check (past the aa_sender check)
		if !strings.Contains(err.Error(), "no RPC client available") {
			t.Errorf("amount=%q: expected 'no RPC client available' error, got: %v", amount, err)
		}
	}
}

func TestETHTransferProcessor_Execute_LogEnrichment(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	testUserAddress := testutil.TestUser1().Address
	processor := NewETHTransferProcessor(vm, nil, nil, &testUserAddress)

	node := &avsproto.ETHTransferNode{
		Config: &avsproto.ETHTransferNode_Config{
			Destination: "not-an-address",
			Amount:      "1000",
		},
	}

	executionLog, err := processor.Execute("test-step", node)

	if err == nil {
		t.Fatal("Expected error for invalid destination")
	}

	// Verify log contains resolved values AND the error
	if !strings.Contains(executionLog.Log, "Resolved: destination=not-an-address, amount=1000") {
		t.Errorf("Expected log to show resolved values, got: %s", executionLog.Log)
	}
	if !strings.Contains(executionLog.Log, "Error: invalid destination address") {
		t.Errorf("Expected log to contain error details, got: %s", executionLog.Log)
	}
}
