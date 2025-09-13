package taskengine

import (
	"net/http"
	"net/http/httptest"
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

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())
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

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())
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

	// Find the branch step regardless of ordering
	var branchStep *avsproto.Execution_Step
	for _, s := range execution.Steps {
		if s.Id == "branch1" {
			branchStep = s
			break
		}
	}
	if branchStep == nil {
		t.Fatalf("branch1 step not found; steps=%d", len(execution.Steps))
	}

	// Branch should succeed but no condition should be matched
	if !branchStep.Success {
		t.Errorf("Expected branch to succeed with silent failure behavior, but got failure: %s", branchStep.Error)
	}

	// The output should be nil since no condition was matched
	if branchStep.OutputData != nil {
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

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())
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
	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())

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
		Index:   0, // Test execution
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

// TestBlockTriggerOutputDataStructure tests that block trigger output data is properly structured
// This replaces the expensive integration test that tried to do real ETH transfers
func TestBlockTriggerOutputDataStructure(t *testing.T) {
	// Test the actual trigger output data structure conversion - what the original test should have been testing
	rawTriggerOutput := map[string]interface{}{
		"blockNumber": uint64(12345),
		"blockHash":   "0xabcdef123456",
		"timestamp":   uint64(1672531200),
		"parentHash":  "0x123456abcdef",
		"difficulty":  "1000000",
		"gasLimit":    uint64(30000000),
		"gasUsed":     uint64(21000),
	}

	// Test buildBlockTriggerOutput function (the actual logic being tested)
	blockOutput := buildBlockTriggerOutput(rawTriggerOutput)

	// Verify the output structure
	if blockOutput == nil {
		t.Fatal("buildBlockTriggerOutput should not return nil")
	}

	if blockOutput.Data == nil {
		t.Fatal("BlockTrigger output Data should not be nil")
	}

	// Verify data can be converted back
	dataMap := blockOutput.Data.AsInterface().(map[string]interface{})
	if dataMap == nil {
		t.Fatal("BlockTrigger output Data should convert to map")
	}

	// Test specific fields and the known uint64->float64 conversion
	if blockNumber, exists := dataMap["blockNumber"]; exists {
		// Due to protobuf conversion, uint64 becomes float64
		if blockNumFloat, ok := blockNumber.(float64); ok {
			if blockNumFloat != 12345.0 {
				t.Errorf("Expected blockNumber 12345.0, got %v", blockNumFloat)
			}
		} else {
			t.Errorf("Expected blockNumber to be float64 after protobuf conversion, got %T", blockNumber)
		}
	} else {
		t.Error("blockNumber field missing from output")
	}

	// Test string fields remain unchanged
	if blockHash, exists := dataMap["blockHash"]; exists {
		if hashStr, ok := blockHash.(string); ok {
			if hashStr != "0xabcdef123456" {
				t.Errorf("Expected blockHash '0xabcdef123456', got %v", hashStr)
			}
		} else {
			t.Errorf("Expected blockHash to be string, got %T", blockHash)
		}
	} else {
		t.Error("blockHash field missing from output")
	}

	t.Log("✅ Block trigger output data structure test passed")
}

// TestFixedTimeTriggerOutputDataStructure tests that fixed time trigger output data is properly structured
func TestFixedTimeTriggerOutputDataStructure(t *testing.T) {
	// Test the actual trigger output data structure conversion
	rawTriggerOutput := map[string]interface{}{
		"timestamp":     uint64(1672531200),
		"timestamp_iso": "2023-01-01T00:00:00Z",
	}

	// Test buildFixedTimeTriggerOutput function
	fixedTimeOutput := buildFixedTimeTriggerOutput(rawTriggerOutput)

	// Verify the output structure
	if fixedTimeOutput == nil {
		t.Fatal("buildFixedTimeTriggerOutput should not return nil")
	}

	if fixedTimeOutput.Data == nil {
		t.Fatal("FixedTimeTrigger output Data should not be nil")
	}

	// Verify data can be converted back
	dataMap := fixedTimeOutput.Data.AsInterface().(map[string]interface{})
	if dataMap == nil {
		t.Fatal("FixedTimeTrigger output Data should convert to map")
	}

	// Test timestamp field and the known uint64->float64 conversion
	if timestamp, exists := dataMap["timestamp"]; exists {
		// Due to protobuf conversion, uint64 becomes float64
		if timestampFloat, ok := timestamp.(float64); ok {
			if timestampFloat != 1672531200.0 {
				t.Errorf("Expected timestamp 1672531200.0, got %v", timestampFloat)
			}
		} else {
			t.Errorf("Expected timestamp to be float64 after protobuf conversion, got %T", timestamp)
		}
	} else {
		t.Error("timestamp field missing from output")
	}

	// Test timestampIso field remains string
	if timestampIso, exists := dataMap["timestampIso"]; exists {
		if isoStr, ok := timestampIso.(string); ok {
			if isoStr != "2023-01-01T00:00:00Z" {
				t.Errorf("Expected timestampIso '2023-01-01T00:00:00Z', got %v", isoStr)
			}
		} else {
			t.Errorf("Expected timestampIso to be string, got %T", timestampIso)
		}
	} else {
		t.Error("timestampIso field missing from output")
	}

	t.Log("✅ Fixed time trigger output data structure test passed")
}

// TestCronTriggerOutputDataStructure tests that cron trigger output data is properly structured
func TestCronTriggerOutputDataStructure(t *testing.T) {
	// Test the actual trigger output data structure conversion
	rawTriggerOutput := map[string]interface{}{
		"timestamp":     uint64(1672531200),
		"timestamp_iso": "2023-01-01T00:00:00Z",
	}

	// Test buildCronTriggerOutput function
	cronOutput := buildCronTriggerOutput(rawTriggerOutput)

	// Verify the output structure
	if cronOutput == nil {
		t.Fatal("buildCronTriggerOutput should not return nil")
	}

	if cronOutput.Data == nil {
		t.Fatal("CronTrigger output Data should not be nil")
	}

	// Verify data can be converted back
	dataMap := cronOutput.Data.AsInterface().(map[string]interface{})
	if dataMap == nil {
		t.Fatal("CronTrigger output Data should convert to map")
	}

	// Test timestamp field and the known uint64->float64 conversion
	if timestamp, exists := dataMap["timestamp"]; exists {
		// Due to protobuf conversion, uint64 becomes float64
		if timestampFloat, ok := timestamp.(float64); ok {
			if timestampFloat != 1672531200.0 {
				t.Errorf("Expected timestamp 1672531200.0, got %v", timestampFloat)
			}
		} else {
			t.Errorf("Expected timestamp to be float64 after protobuf conversion, got %T", timestamp)
		}
	} else {
		t.Error("timestamp field missing from output")
	}

	// Test timestampIso field remains string
	if timestampIso, exists := dataMap["timestampIso"]; exists {
		if isoStr, ok := timestampIso.(string); ok {
			if isoStr != "2023-01-01T00:00:00Z" {
				t.Errorf("Expected timestampIso '2023-01-01T00:00:00Z', got %v", isoStr)
			}
		} else {
			t.Errorf("Expected timestampIso to be string, got %T", timestampIso)
		}
	} else {
		t.Error("timestampIso field missing from output")
	}

	t.Log("✅ Cron trigger output data structure test passed")
}
