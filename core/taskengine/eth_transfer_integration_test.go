package taskengine

import (
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func TestETHTransferTaskIntegration(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	user := testutil.TestUser1()

	// Create an ETH transfer task
	tr := &avsproto.CreateTaskReq{
		Name:               "ETH Transfer Test",
		SmartWalletAddress: "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6",
		Trigger: &avsproto.TaskTrigger{
			Id:          "manual_trigger",
			Name:        "manual",
			TriggerType: &avsproto.TaskTrigger_Manual{Manual: true},
		},
		Nodes: []*avsproto.TaskNode{
			{
				Id:   "eth_transfer_1",
				Name: "Transfer ETH",
				TaskType: &avsproto.TaskNode_EthTransfer{
					EthTransfer: &avsproto.ETHTransferNode{
						Config: &avsproto.ETHTransferNode_Config{
							Destination: "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6",
							Amount:      "1000000000000000000", // 1 ETH in wei
						},
					},
				},
			},
		},
		Edges: []*avsproto.TaskEdge{
			{
				Id:     "edge_1",
				Source: "manual_trigger", // Edge from trigger to node
				Target: "eth_transfer_1",
			},
		},
	}

	// Create the task
	task, err := n.CreateTask(user, tr)
	if err != nil {
		t.Fatalf("Failed to create ETH transfer task: %v", err)
	}

	if task == nil {
		t.Fatal("Created task is nil")
	}

	if task.Id == "" {
		t.Error("Task ID should not be empty")
	}

	if task.Name != "ETH Transfer Test" {
		t.Errorf("Expected task name 'ETH Transfer Test', got '%s'", task.Name)
	}

	// Trigger the task
	triggerResult, err := n.TriggerTask(user, &avsproto.TriggerTaskReq{
		TaskId:      task.Id,
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerOutput: &avsproto.TriggerTaskReq_ManualTrigger{
			ManualTrigger: &avsproto.ManualTrigger_Output{},
		},
		IsBlocking: true, // Execute synchronously for testing
	})

	if err != nil {
		t.Fatalf("Failed to trigger ETH transfer task: %v", err)
	}

	if triggerResult == nil {
		t.Fatal("Trigger result is nil")
	}

	if triggerResult.ExecutionId == "" {
		t.Error("Execution ID should not be empty")
	}

	// Get the execution details
	execution, err := n.GetExecution(user, &avsproto.ExecutionReq{
		TaskId:      task.Id,
		ExecutionId: triggerResult.ExecutionId,
	})

	if err != nil {
		t.Fatalf("Failed to get execution: %v", err)
	}

	if execution == nil {
		t.Fatal("Execution is nil")
	}

	if !execution.Success {
		t.Errorf("Expected execution to succeed, got success=%v, error=%s", execution.Success, execution.Error)
	}

	if len(execution.Steps) == 0 {
		t.Fatal("Expected at least one execution step")
	}

	// With unified API, we expect 2 steps: trigger step + node step
	if len(execution.Steps) != 2 {
		t.Fatalf("Expected 2 execution steps (trigger + node), got %d", len(execution.Steps))
	}

	// Check the trigger step (index 0)
	triggerStep := execution.Steps[0]
	if triggerStep.Id != "manual_trigger" {
		t.Errorf("Expected trigger step ID 'manual_trigger', got '%s'", triggerStep.Id)
	}

	if !triggerStep.Success {
		t.Errorf("Expected trigger step to succeed, got success=%v, error=%s", triggerStep.Success, triggerStep.Error)
	}

	// Check the ETH transfer step (index 1)
	step := execution.Steps[1]
	if step.Id != "eth_transfer_1" {
		t.Errorf("Expected step NodeId 'eth_transfer_1', got '%s'", step.Id)
	}

	if !step.Success {
		t.Errorf("Expected step to succeed, got success=%v, error=%s", step.Success, step.Error)
	}

	// Check ETH transfer output
	ethTransferOutput := step.GetEthTransfer()
	if ethTransferOutput == nil {
		t.Fatal("Expected ETH transfer output data")
	}

	if ethTransferOutput.TransactionHash == "" {
		t.Error("Expected transaction hash to be set")
	}

	t.Logf("ETH transfer task executed successfully with transaction hash: %s", ethTransferOutput.TransactionHash)
}

func TestETHTransferTaskWithInvalidConfig(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	user := testutil.TestUser1()

	// Create an ETH transfer task with invalid destination
	tr := &avsproto.CreateTaskReq{
		Name:               "Invalid ETH Transfer Test",
		SmartWalletAddress: "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6",
		Trigger: &avsproto.TaskTrigger{
			Id:          "manual_trigger",
			Name:        "manual",
			TriggerType: &avsproto.TaskTrigger_Manual{Manual: true},
		},
		Nodes: []*avsproto.TaskNode{
			{
				Id:   "eth_transfer_1",
				Name: "Transfer ETH",
				TaskType: &avsproto.TaskNode_EthTransfer{
					EthTransfer: &avsproto.ETHTransferNode{
						Config: &avsproto.ETHTransferNode_Config{
							Destination: "invalid-address", // Invalid address
							Amount:      "1000000000000000000",
						},
					},
				},
			},
		},
		Edges: []*avsproto.TaskEdge{
			{
				Id:     "edge_1",
				Source: "manual_trigger", // Edge from trigger to node
				Target: "eth_transfer_1",
			},
		},
	}

	// Create the task (should succeed - validation happens at execution time)
	task, err := n.CreateTask(user, tr)
	if err != nil {
		t.Fatalf("Failed to create ETH transfer task: %v", err)
	}

	// Trigger the task (should succeed as a call, but execution should fail)
	triggerResult, err := n.TriggerTask(user, &avsproto.TriggerTaskReq{
		TaskId:      task.Id,
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerOutput: &avsproto.TriggerTaskReq_ManualTrigger{
			ManualTrigger: &avsproto.ManualTrigger_Output{},
		},
		IsBlocking: true,
	})

	// TriggerTask should succeed but the execution should fail
	if err != nil {
		t.Fatalf("TriggerTask should not fail at the API level, but execution should fail: %v", err)
	}

	if triggerResult == nil {
		t.Fatal("Trigger result should not be nil")
	}

	if triggerResult.ExecutionId == "" {
		t.Error("Execution ID should not be empty")
	}

	// Get the execution details to check that it failed with the expected error
	execution, err := n.GetExecution(user, &avsproto.ExecutionReq{
		TaskId:      task.Id,
		ExecutionId: triggerResult.ExecutionId,
	})

	if err != nil {
		t.Fatalf("Failed to get execution: %v", err)
	}

	if execution == nil {
		t.Fatal("Execution is nil")
	}

	// The execution should have failed
	if execution.Success {
		t.Error("Expected execution to fail due to invalid destination address")
		return
	}

	// Check that the overall execution indicates failure
	if execution.Error == "" {
		t.Error("Expected execution to have an error message")
	}

	// Verify that we have the expected steps
	if len(execution.Steps) < 2 {
		t.Fatalf("Expected at least 2 steps (trigger + node), got %d", len(execution.Steps))
	}

	// Check that the ETH transfer step failed
	ethTransferStep := execution.Steps[1] // Second step should be the ETH transfer
	if ethTransferStep.Id != "eth_transfer_1" {
		t.Errorf("Expected second step to be 'eth_transfer_1', got '%s'", ethTransferStep.Id)
	}

	if ethTransferStep.Success {
		t.Error("Expected ETH transfer step to fail")
	}

	// Check that the step error message contains the expected validation error
	expectedError := "invalid destination address"
	if !strings.Contains(ethTransferStep.Error, expectedError) {
		t.Errorf("Expected step error to contain '%s', got: %s", expectedError, ethTransferStep.Error)
	}

	t.Logf("ETH transfer task correctly failed with error: %s", execution.Error)
}
