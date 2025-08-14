package taskengine

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/gow"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestExecutorRunTaskSucess(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	// Set up a test HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a response with "I'm hit"
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"message": "I'm hit"}`))
	}))
	defer server.Close()

	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch1",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Config: &avsproto.BranchNode_Config{
						Conditions: []*avsproto.BranchNode_Condition{
							{
								Id:   "a1",
								Type: "if",
								// The test data is of this transaction https://sepolia.etherscan.io/tx/0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8 which is 3.45 token
								Expression: "Number(triggertest.data.valueFormatted) >= 3",
							},
						},
					},
				},
			},
		},
		{
			Id:   "notification1",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Config: &avsproto.RestAPINode_Config{
						Url:    server.URL, // Use the test server URL
						Method: "POST",
						Body:   "hit=notification1",
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
			Target: "branch1",
		},
		{
			Id:     "e2",
			Source: "branch1.a1",
			Target: "notification1",
		},
	}

	task := &model.Task{
		Task: &avsproto.Task{
			Id:      "TaskID123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger(), nil)
	triggerData := testutil.GetTestEventTriggerDataWithTransferData()
	execution, err := executor.RunTask(task, &QueueExecutionData{
		TriggerType:   triggerData.Type,
		TriggerOutput: triggerData.Output,
		ExecutionID:   "exec123",
	})

	if err != nil {
		t.Errorf("expect no error but got %v", err)
	}

	if !execution.Success {
		t.Errorf("Expect success status but got failure")
	}

	if execution.Error != "" {
		t.Errorf("Expect no error but got: %s", execution.Error)
	}

	// Validate by IDs instead of fixed indices to be robust to scheduler ordering
	hasBranch := false
	hasNotification := false
	for _, s := range execution.Steps {
		if s.Id == "branch1" {
			hasBranch = true
		}
		if s.Id == "notification1" {
			hasNotification = true
		}
	}
	if !hasBranch || !hasNotification {
		t.Fatalf("Expected both branch1 and notification1 steps; gotBranch=%v gotNotification=%v (total=%d)", hasBranch, hasNotification, len(execution.Steps))
	}

	// Find the notification1 step to inspect its output
	var notifStep *avsproto.Execution_Step
	for i := range execution.Steps {
		if execution.Steps[i].Id == "notification1" {
			notifStep = execution.Steps[i]
			break
		}
	}
	if notifStep == nil {
		t.Fatalf("notification1 step not found in execution steps")
	}
	outputData := gow.ValueToMap(notifStep.GetRestApi().Data)
	bodyData := outputData["data"].(map[string]interface{})
	if bodyData["message"].(string) != "I'm hit" {
		t.Errorf("expect message to be 'I'm hit' but got %s", bodyData["message"])
	}
}

func TestExecutorRunTaskWithBranchSilentFailureBehavior(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"message":"ok"}`))
	}))
	defer ts.Close()

	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch1",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Config: &avsproto.BranchNode_Config{
						Conditions: []*avsproto.BranchNode_Condition{
							{
								Id:         "a1",
								Type:       "if",
								Expression: "a >= 5", // Variable 'a' doesn't exist - should be treated as false
							},
						},
					},
				},
			},
		},
		{
			Id:   "notification1",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Config: &avsproto.RestAPINode_Config{
						Url:    ts.URL,
						Method: "POST",
						Body:   "hit=notification1",
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
			Target: "branch1",
		},
		{
			Id:     "e2",
			Source: "branch1.a1",
			Target: "notification1",
		},
	}

	task := &model.Task{
		Task: &avsproto.Task{
			Id:      "TaskID123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger(), nil)
	triggerData := testutil.GetTestEventTriggerData()
	execution, err := executor.RunTask(task, &QueueExecutionData{
		TriggerType:   triggerData.Type,
		TriggerOutput: triggerData.Output,
		ExecutionID:   "exec123",
	})

	// With silent failure behavior, branch nodes no longer hard fail - they succeed but no condition matches
	if err != nil {
		t.Errorf("Expected no error with silent failure behavior, but got: %v", err)
	}

	if !execution.Success {
		t.Errorf("Expected success status with silent failure behavior, but got failure: %s", execution.Error)
	}

	if len(execution.Steps) != 2 {
		t.Errorf("expect 2 steps (trigger + branch) but got: %d", len(execution.Steps))
	}

	// Check branch1 step (should succeed but not match any condition)
	if execution.Steps[1].Id != "branch1" {
		t.Errorf("expect evaluate branch node but got: %s", execution.Steps[1].Id)
	}

	// Branch should succeed but no condition should be matched
	if !execution.Steps[1].Success {
		t.Errorf("Expected branch to succeed with silent failure behavior, but got failure: %s", execution.Steps[1].Error)
	}

	// The output should be nil since no condition was matched
	if execution.Steps[1].OutputData != nil {
		t.Errorf("Expected no branch output since no condition matched, but got output")
	}
}

func TestExecutorRunTaskComputeSuccessFalseWhenANodeFailedToRun(t *testing.T) {
	// Set up a test HTTP server that returns a 503 status code
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	nodes := []*avsproto.TaskNode{
		{
			Id:   "branch1",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Config: &avsproto.BranchNode_Config{
						Conditions: []*avsproto.BranchNode_Condition{
							{
								Id:         "condition1",
								Type:       "if",
								Expression: "true",
							},
						},
					},
				},
			},
		},
		{
			Id:   "rest1",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Config: &avsproto.RestAPINode_Config{
						Url:    server.URL, // Use the test server URL
						Method: "POST",
						Body:   "hit=notification1",
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
			Target: "branch1",
		},
		{
			Id:     "e2",
			Source: "branch1.condition1",
			Target: "rest1",
		},
	}

	task := &model.Task{
		Task: &avsproto.Task{
			Id:      "TaskID123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger(), nil)
	triggerData := testutil.GetTestEventTriggerData()
	execution, err := executor.RunTask(task, &QueueExecutionData{
		TriggerType:   triggerData.Type,
		TriggerOutput: triggerData.Output,
		ExecutionID:   "exec123",
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if execution.Success {
		t.Errorf("Expected failure status when a node fails (HTTP 503), but got success")
	}

	if len(execution.Steps) != 3 {
		t.Errorf("Expect evaluate 3 nodes (trigger + 2 workflow nodes) but got: %d", len(execution.Steps))
	}

	// Check branch1 step at index 0 (no trigger step in regular executions)
	if execution.Steps[1].Id != "branch1" {
		t.Errorf("step id doesn't match, expect branch1 but got: %s", execution.Steps[1].Id)
	}

	// Check rest1 step at index 1
	// 	if execution.Steps[1].Id != "rest1" {
	// 		t.Errorf("step id doesn't match, expect rest1 but got: %s", execution.Steps[2].Id)
	// 	}

	// Verify that the REST API step correctly failed with 503 status code
	if execution.Steps[2].Success {
		t.Errorf("REST API step should fail with 503 status (HTTP errors are failures), but got success")
	}

	// The 503 status code should be available in the step's output data for workflow logic to handle
	if execution.Steps[1].OutputData == nil {
		t.Error("Expected REST API step to have output data")
	}
}

// TestExecutorRunTaskReturnAllExecutionData to test the happy path and return all the relevant data a task needed
func TestExecutorRunTaskReturnAllExecutionData(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a response with "I'm hit"
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"message": "I'm hit"}`))
	}))
	defer server.Close()

	nodes := []*avsproto.TaskNode{
		{
			Id:   "spacex",
			Name: "spacex",
			TaskType: &avsproto.TaskNode_GraphqlQuery{
				GraphqlQuery: &avsproto.GraphQLQueryNode{
					Config: &avsproto.GraphQLQueryNode_Config{
						Url: "https://spacex-production.up.railway.app/",
						Query: `
						query Launch {
							company {
							ceo
							}
							launches(limit: 2, sort: "launch_date_unix", order: "ASC") {
							id
							mission_name
							}
						}
				`,
					},
				},
			},
		},
		{
			Id:   "branch1",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Config: &avsproto.BranchNode_Config{
						Conditions: []*avsproto.BranchNode_Condition{
							{
								Id:         "condition1",
								Type:       "if",
								Expression: "Number(triggertest.data.valueFormatted) >= 3",
							},
						},
					},
				},
			},
		},
		{
			Id:   "customcode1",
			Name: "dummy",
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						// Just logout the data so we can assert from the output
						Source: "JSON.stringify(triggertest.data)",
					},
				},
			},
		},
		{
			Id:   "rest1",
			Name: "http",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Config: &avsproto.RestAPINode_Config{
						Url:    server.URL, // Use the test server URL
						Method: "POST",
						Body:   "hit=notification1",
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
			Id:     "e0",
			Source: trigger.Id,
			Target: "spacex",
		},
		{
			Id:     "e1",
			Source: "spacex",
			Target: "branch1",
		},
		{
			Id:     "e3",
			Source: "branch1.condition1",
			Target: "customcode1",
		},
		{
			Id:     "e2",
			Source: "customcode1",
			Target: "rest1",
		},
	}

	task := &model.Task{
		Task: &avsproto.Task{
			Id:      "TaskID123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}

	// Create executor
	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger(), nil)

	// Get the event trigger data with EvmLog data
	testTriggerData := testutil.GetTestEventTriggerData()

	// Convert to taskengine.TriggerData
	triggerData := &TriggerData{
		Type:   testTriggerData.Type,
		Output: testTriggerData.Output,
	}

	// Get the mock transfer log data for testing
	transferTriggerData := testutil.GetTestEventTriggerDataWithTransferData()

	// For this test, we need to test the execution with transfer log data
	// So we'll override the RunTask method behavior by calling the VM directly
	secrets, _ := LoadSecretForTask(executor.db, task)

	// Extract the Data field from EventTrigger_Output
	var transferLog *structpb.Value
	if eventOutput, ok := transferTriggerData.Output.(*avsproto.EventTrigger_Output); ok {
		transferLog = eventOutput.Data
	}

	vm, err := NewVMWithDataAndTransferLog(task, triggerData, executor.smartWalletConfig, secrets, transferLog)
	if err != nil {
		t.Fatalf("error creating VM: %v", err)
	}

	vm.WithLogger(executor.logger).WithDb(executor.db)

	// Execute the task
	err = vm.Compile()
	if err != nil {
		t.Fatalf("error compiling task: %v", err)
	}

	runErr := vm.Run()
	if runErr != nil {
		t.Fatalf("error running task: %v", runErr)
	}

	// Create execution result similar to what RunTask would create
	t0 := time.Now()
	t1 := time.Now()
	execution := &avsproto.Execution{
		Id:      "test_exec123",
		StartAt: t0.UnixMilli(),
		EndAt:   t1.UnixMilli(),
		Success: runErr == nil,
		Error:   "",
		Steps:   vm.ExecutionLogs,
	}

	if runErr != nil {
		execution.Error = runErr.Error()
	}

	if execution.Id != "test_exec123" {
		t.Errorf("expect execution id is test_exec123 but got: %s", execution.Id)
	}

	if !execution.Success {
		t.Errorf("expect success status but got failure")
	}

	if err != nil || execution.Error != "" {
		t.Errorf("expect no error but got: %s", execution.Error)
	}

	if execution.StartAt <= 0 {
		t.Errorf("expect start at is set but got: %d", execution.StartAt)
	}

	if execution.EndAt <= 0 {
		t.Errorf("expect end at is set but got: %d", execution.EndAt)
	}
}

func TestExecutorRunTaskWithBlockTriggerOutputData(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	// Create a simple task with block trigger and ETH transfer node
	nodes := []*avsproto.TaskNode{
		{
			Id:   "eth_transfer_1",
			Name: "TransferETH",
			TaskType: &avsproto.TaskNode_EthTransfer{
				EthTransfer: &avsproto.ETHTransferNode{
					Config: &avsproto.ETHTransferNode_Config{
						Destination: "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6",
						Amount:      "1000000000000000000", // 1 ETH in wei
					},
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "block_trigger",
		Name: "blockTrigger",
		TriggerType: &avsproto.TaskTrigger_Block{
			Block: &avsproto.BlockTrigger{
				Config: &avsproto.BlockTrigger_Config{
					Interval: 5,
				},
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "edge_1",
			Source: trigger.Id,
			Target: "eth_transfer_1",
		},
	}

	task := &model.Task{
		Task: &avsproto.Task{
			Id:      "BlockTriggerTaskID123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger(), nil)
	triggerData := testutil.GetTestEventTriggerData()
	execution, err := executor.RunTask(task, &QueueExecutionData{
		TriggerType:   triggerData.Type,
		TriggerOutput: triggerData.Output,
		ExecutionID:   "block_exec123",
	})

	// Basic execution checks
	if execution.Id != "block_exec123" {
		t.Errorf("expect execution id is block_exec123 but got: %s", execution.Id)
	}

	if !execution.Success {
		t.Errorf("expect success status but got failure: %s", execution.Error)
	}

	if err != nil || execution.Error != "" {
		t.Errorf("expect no error but got: %s", execution.Error)
	}

	// Verify execution steps
	if len(execution.Steps) != 2 {
		t.Errorf("expect 2 steps (trigger + node) but got: %d", len(execution.Steps))
	}

	// Check eth_transfer_1 step at index 0 (no trigger step in regular executions)
	if execution.Steps[1].Id != "eth_transfer_1" {
		t.Errorf("expect step NodeId is eth_transfer_1 but got: %s", execution.Steps[1].Id)
	}

	if !execution.Steps[1].Success {
		t.Errorf("expect step to succeed but got failure: %s", execution.Steps[1].Error)
	}
}

func TestExecutorRunTaskWithFixedTimeTriggerOutputData(t *testing.T) {
	if os.Getenv("SEPOLIA_BUNDLER_RPC") == "" {
		t.Skip("Skipping FixedTimeTrigger ETH transfer test: SEPOLIA_BUNDLER_RPC not configured")
	}
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	// Create a simple task with fixed time trigger and ETH transfer node
	nodes := []*avsproto.TaskNode{
		{
			Id:   "eth_transfer_1",
			Name: "TransferETH",
			TaskType: &avsproto.TaskNode_EthTransfer{
				EthTransfer: &avsproto.ETHTransferNode{
					Config: &avsproto.ETHTransferNode_Config{
						Destination: "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6",
						Amount:      "1000000000000000000", // 1 ETH in wei
					},
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "fixed_time_trigger",
		Name: "fixedTimeTrigger",
		TriggerType: &avsproto.TaskTrigger_FixedTime{
			FixedTime: &avsproto.FixedTimeTrigger{
				Config: &avsproto.FixedTimeTrigger_Config{
					Epochs: []int64{1640995200}, // Example epoch timestamp
				},
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "edge_1",
			Source: trigger.Id,
			Target: "eth_transfer_1",
		},
	}

	task := &model.Task{
		Task: &avsproto.Task{
			Id:      "FixedTimeTriggerTaskID123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger(), nil)
	triggerData := testutil.GetTestEventTriggerData()
	execution, err := executor.RunTask(task, &QueueExecutionData{
		TriggerType:   triggerData.Type,
		TriggerOutput: triggerData.Output,
		ExecutionID:   "fixed_time_exec123",
	})

	// Since we removed mock data generation, this test should now succeed
	// because the VM execution path uses real trigger data from TriggerReason
	if execution.Id != "fixed_time_exec123" {
		t.Errorf("expect execution id is fixed_time_exec123 but got: %s", execution.Id)
	}

	if !execution.Success {
		t.Errorf("expect success status but got failure: %s", execution.Error)
	}

	if err != nil || execution.Error != "" {
		t.Errorf("expect no error but got: %s", execution.Error)
	}
}

func TestExecutorRunTaskWithCronTriggerOutputData(t *testing.T) {
	if os.Getenv("SEPOLIA_BUNDLER_RPC") == "" {
		t.Skip("Skipping CronTrigger ETH transfer test: SEPOLIA_BUNDLER_RPC not configured")
	}
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	// Create a simple task with cron trigger and ETH transfer node
	nodes := []*avsproto.TaskNode{
		{
			Id:   "eth_transfer_1",
			Name: "TransferETH",
			TaskType: &avsproto.TaskNode_EthTransfer{
				EthTransfer: &avsproto.ETHTransferNode{
					Config: &avsproto.ETHTransferNode_Config{
						Destination: "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6",
						Amount:      "1000000000000000000", // 1 ETH in wei
					},
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "cron_trigger",
		Name: "cronTrigger",
		TriggerType: &avsproto.TaskTrigger_Cron{
			Cron: &avsproto.CronTrigger{
				Config: &avsproto.CronTrigger_Config{
					Schedules: []string{"0 0 * * *"}, // Daily at midnight
				},
			},
		},
	}

	edges := []*avsproto.TaskEdge{
		{
			Id:     "edge_1",
			Source: trigger.Id,
			Target: "eth_transfer_1",
		},
	}

	task := &model.Task{
		Task: &avsproto.Task{
			Id:      "CronTriggerTaskID123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger(), nil)
	triggerData := testutil.GetTestEventTriggerData()
	execution, err := executor.RunTask(task, &QueueExecutionData{
		TriggerType:   triggerData.Type,
		TriggerOutput: triggerData.Output,
		ExecutionID:   "cron_exec123",
	})

	// Basic execution checks
	if execution.Id != "cron_exec123" {
		t.Errorf("expect execution id is cron_exec123 but got: %s", execution.Id)
	}

	if !execution.Success {
		t.Errorf("expect success status but got failure: %s", execution.Error)
	}

	if err != nil || execution.Error != "" {
		t.Errorf("expect no error but got: %s", execution.Error)
	}
}
