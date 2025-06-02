package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestETHTransferProcessor_Execute_Success(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	processor := NewETHTransferProcessor(vm, nil, nil, testutil.TestUser1().Address)

	node := &avsproto.ETHTransferNode{
		Config: &avsproto.ETHTransferNode_Config{
			Destination: "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6",
			Amount:      "1000000000000000000", // 1 ETH in wei
		},
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

	if ethTransferOutput.TransactionHash == "" {
		t.Error("Expected transaction hash to be set")
	}

	// Check that output variables were set
	outputVar := processor.GetOutputVar("test-step")
	if outputVar == nil {
		t.Fatal("Expected output variable to be set")
	}

	outputMap, ok := outputVar.(map[string]interface{})
	if !ok {
		t.Fatal("Expected output variable to be a map")
	}

	if outputMap["destination"] != "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6" {
		t.Errorf("Expected destination in output, got: %v", outputMap["destination"])
	}

	if outputMap["amount"] != "1000000000000000000" {
		t.Errorf("Expected amount in output, got: %v", outputMap["amount"])
	}

	if outputMap["success"] != true {
		t.Errorf("Expected success=true in output, got: %v", outputMap["success"])
	}
}

func TestETHTransferProcessor_Execute_InvalidDestination(t *testing.T) {
	vm := NewVM()
	vm.WithLogger(testutil.GetLogger())

	processor := NewETHTransferProcessor(vm, nil, nil, testutil.TestUser1().Address)

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

	processor := NewETHTransferProcessor(vm, nil, nil, testutil.TestUser1().Address)

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

	processor := NewETHTransferProcessor(vm, nil, nil, testutil.TestUser1().Address)

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

	processor := NewETHTransferProcessor(vm, nil, nil, testutil.TestUser1().Address)

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

	processor := NewETHTransferProcessor(vm, nil, nil, testutil.TestUser1().Address)

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
