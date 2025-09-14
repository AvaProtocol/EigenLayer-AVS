package taskengine

import (
	"strings"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"google.golang.org/protobuf/encoding/protojson"
)

// TestAnalyzeExecutionResult_AllSuccess tests the case where all steps succeed
func TestAnalyzeExecutionResult_AllSuccess(t *testing.T) {
	vm := NewVM()
	vm.logger = testutil.GetLogger()

	// Create execution logs with all successful steps
	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id:      "trigger1",
			Success: true,
			Error:   "",
			Name:    "Manual Trigger",
		},
		{
			Id:      "step1",
			Success: true,
			Error:   "",
			Name:    "HTTP Request",
		},
		{
			Id:      "step2",
			Success: true,
			Error:   "",
			Name:    "Custom Code",
		},
	}

	success, errorMessage, failedCount, resultStatus := vm.AnalyzeExecutionResult()

	// Verify results
	if !success {
		t.Errorf("Expected success=true, got success=%v", success)
	}
	if errorMessage != "" {
		t.Errorf("Expected empty error message, got: %s", errorMessage)
	}
	if failedCount != 0 {
		t.Errorf("Expected failedCount=0, got failedCount=%d", failedCount)
	}
	if resultStatus != ExecutionSuccess {
		t.Errorf("Expected resultStatus=ExecutionSuccess, got resultStatus=%v", resultStatus)
	}
}

// TestAnalyzeExecutionResult_PartialSuccess tests the case where some steps succeed and some fail
func TestAnalyzeExecutionResult_PartialSuccess(t *testing.T) {
	vm := NewVM()
	vm.logger = testutil.GetLogger()

	// Create execution logs with mixed success and failure
	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id:      "trigger1",
			Success: true,
			Error:   "",
			Name:    "Manual Trigger",
		},
		{
			Id:      "step1",
			Success: true,
			Error:   "",
			Name:    "HTTP Request",
		},
		{
			Id:      "step2",
			Success: false,
			Error:   "Connection timeout",
			Name:    "Database Query",
		},
		{
			Id:      "step3",
			Success: true,
			Error:   "",
			Name:    "Final Notification",
		},
	}

	success, errorMessage, failedCount, resultStatus := vm.AnalyzeExecutionResult()

	// Verify results
	if success {
		t.Errorf("Expected success=false for partial success, got success=%v", success)
	}
	if errorMessage == "" {
		t.Errorf("Expected non-empty error message for partial success")
	}
	if failedCount != 1 {
		t.Errorf("Expected failedCount=1, got failedCount=%d", failedCount)
	}
	if resultStatus != ExecutionPartialSuccess {
		t.Errorf("Expected resultStatus=ExecutionPartialSuccess, got resultStatus=%v", resultStatus)
	}

	// Check that error message contains partial success information
	expectedSubstring := "Partial success: 1 of 4 steps failed"
	if len(errorMessage) == 0 || errorMessage[:len(expectedSubstring)] != expectedSubstring {
		t.Errorf("Expected error message to start with '%s', got: %s", expectedSubstring, errorMessage)
	}
}

// TestAnalyzeExecutionResult_AllFailure tests the case where all steps fail
func TestAnalyzeExecutionResult_AllFailure(t *testing.T) {
	vm := NewVM()
	vm.logger = testutil.GetLogger()

	// Create execution logs with all failed steps
	vm.ExecutionLogs = []*avsproto.Execution_Step{
		{
			Id:      "trigger1",
			Success: false,
			Error:   "Trigger condition not met",
			Name:    "Event Trigger",
		},
		{
			Id:      "step1",
			Success: false,
			Error:   "HTTP 500 error",
			Name:    "HTTP Request",
		},
		{
			Id:      "step2",
			Success: false,
			Error:   "JavaScript syntax error",
			Name:    "Custom Code",
		},
	}

	success, errorMessage, failedCount, resultStatus := vm.AnalyzeExecutionResult()

	// Verify results
	if success {
		t.Errorf("Expected success=false for all failures, got success=%v", success)
	}
	if errorMessage == "" {
		t.Errorf("Expected non-empty error message for all failures")
	}
	if failedCount != 3 {
		t.Errorf("Expected failedCount=3, got failedCount=%d", failedCount)
	}
	if resultStatus != ExecutionFailure {
		t.Errorf("Expected resultStatus=ExecutionFailure, got resultStatus=%v", resultStatus)
	}

	// Check that error message contains all failures information
	expectedSubstring := "All: 3 of 3 steps failed"
	if len(errorMessage) == 0 || !strings.Contains(errorMessage, expectedSubstring) {
		t.Errorf("Expected error message to contain '%s', got: %s", expectedSubstring, errorMessage)
	}
}

// TestAnalyzeExecutionResult_NoSteps tests the edge case where there are no execution steps
func TestAnalyzeExecutionResult_NoSteps(t *testing.T) {
	vm := NewVM()
	vm.logger = testutil.GetLogger()

	// No execution logs
	vm.ExecutionLogs = []*avsproto.Execution_Step{}

	success, errorMessage, failedCount, resultStatus := vm.AnalyzeExecutionResult()

	// Verify results
	if success {
		t.Errorf("Expected success=false for no steps, got success=%v", success)
	}
	if errorMessage != "no execution steps found" {
		t.Errorf("Expected specific error message for no steps, got: %s", errorMessage)
	}
	if failedCount != 0 {
		t.Errorf("Expected failedCount=0 for no steps, got failedCount=%d", failedCount)
	}
	if resultStatus != ExecutionFailure {
		t.Errorf("Expected resultStatus=ExecutionFailure for no steps, got resultStatus=%v", resultStatus)
	}
}

// TestGetExecutionStatus_PartialSuccess tests the GetExecutionStatus method for partial success
func TestGetExecutionStatus_PartialSuccess(t *testing.T) {
	// Set up test database and engine
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// Create a test user
	user := testutil.TestUser1()

	// Create a test task
	task := &model.Task{
		Task: &avsproto.Task{
			Id:     "test-task-id",
			Owner:  user.Address.Hex(),
			Status: avsproto.TaskStatus_Active,
			Name:   "Test Task",
		},
	}

	// Create execution with partial success (some steps succeed, some fail)
	execution := &avsproto.Execution{
		Id:      "test-execution-id",
		StartAt: time.Now().UnixMilli(),
		EndAt:   time.Now().UnixMilli(),
		Status:  avsproto.ExecutionStatus_EXECUTION_STATUS_PARTIAL_SUCCESS, // Overall status is partial success
		Error:   "Partial success: 1 of 3 steps failed: Database Query",
		Index:   0, // First execution
		Steps: []*avsproto.Execution_Step{
			{
				Id:      "trigger1",
				Success: true,
				Error:   "",
				Name:    "Manual Trigger",
			},
			{
				Id:      "step1",
				Success: true,
				Error:   "",
				Name:    "HTTP Request",
			},
			{
				Id:      "step2",
				Success: false,
				Error:   "Connection timeout",
				Name:    "Database Query",
			},
		},
	}

	// Store the task and execution in the database
	taskJSON, err := task.ToJSON()
	if err != nil {
		t.Fatalf("Failed to serialize task: %v", err)
	}
	err = db.Set(TaskStorageKey(task.Id, task.Status), taskJSON)
	if err != nil {
		t.Fatalf("Failed to store task: %v", err)
	}

	// Store the execution using protojson
	executionJSON, err := protojson.Marshal(execution)
	if err != nil {
		t.Fatalf("Failed to serialize execution: %v", err)
	}
	err = db.Set(TaskExecutionKey(task, execution.Id), executionJSON)
	if err != nil {
		t.Fatalf("Failed to store execution: %v", err)
	}

	// Test GetExecutionStatus
	statusResp, err := engine.GetExecutionStatus(user, &avsproto.ExecutionReq{
		TaskId:      task.Id,
		ExecutionId: execution.Id,
	})

	if err != nil {
		t.Fatalf("GetExecutionStatus failed: %v", err)
	}

	// Verify that it returns PARTIAL_SUCCESS status
	if statusResp.Status != avsproto.ExecutionStatus_EXECUTION_STATUS_PARTIAL_SUCCESS {
		t.Errorf("Expected EXECUTION_STATUS_PARTIAL_SUCCESS, got %v", statusResp.Status)
	}
}

// TestGetExecutionStatus_FullSuccess tests the GetExecutionStatus method for full success
func TestGetExecutionStatus_FullSuccess(t *testing.T) {
	// Set up test database and engine
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// Create a test user
	user := testutil.TestUser1()

	// Create a test task
	task := &model.Task{
		Task: &avsproto.Task{
			Id:     "test-task-id",
			Owner:  user.Address.Hex(),
			Status: avsproto.TaskStatus_Active,
			Name:   "Test Task",
		},
	}

	// Create execution with full success
	execution := &avsproto.Execution{
		Id:      "test-execution-id",
		StartAt: time.Now().UnixMilli(),
		EndAt:   time.Now().UnixMilli(),
		Status:  avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS, // Overall status is success
		Error:   "",
		Index:   0, // First execution
		Steps: []*avsproto.Execution_Step{
			{
				Id:      "trigger1",
				Success: true,
				Error:   "",
				Name:    "Manual Trigger",
			},
			{
				Id:      "step1",
				Success: true,
				Error:   "",
				Name:    "HTTP Request",
			},
		},
	}

	// Store the task and execution in the database
	taskJSON, err := task.ToJSON()
	if err != nil {
		t.Fatalf("Failed to serialize task: %v", err)
	}
	err = db.Set(TaskStorageKey(task.Id, task.Status), taskJSON)
	if err != nil {
		t.Fatalf("Failed to store task: %v", err)
	}

	// Store the execution using protojson
	executionJSON, err := protojson.Marshal(execution)
	if err != nil {
		t.Fatalf("Failed to serialize execution: %v", err)
	}
	err = db.Set(TaskExecutionKey(task, execution.Id), executionJSON)
	if err != nil {
		t.Fatalf("Failed to store execution: %v", err)
	}

	// Test GetExecutionStatus
	statusResp, err := engine.GetExecutionStatus(user, &avsproto.ExecutionReq{
		TaskId:      task.Id,
		ExecutionId: execution.Id,
	})

	if err != nil {
		t.Fatalf("GetExecutionStatus failed: %v", err)
	}

	// Verify that it returns SUCCESS status
	if statusResp.Status != avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS {
		t.Errorf("Expected EXECUTION_STATUS_SUCCESS, got %v", statusResp.Status)
	}
}

// TestGetExecutionStatus_FullFailure tests the GetExecutionStatus method for full failure
func TestGetExecutionStatus_FullFailure(t *testing.T) {
	// Set up test database and engine
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// Create a test user
	user := testutil.TestUser1()

	// Create a test task
	task := &model.Task{
		Task: &avsproto.Task{
			Id:     "test-task-id",
			Owner:  user.Address.Hex(),
			Status: avsproto.TaskStatus_Active,
			Name:   "Test Task",
		},
	}

	// Create execution with all failures
	execution := &avsproto.Execution{
		Id:      "test-execution-id",
		StartAt: time.Now().UnixMilli(),
		EndAt:   time.Now().UnixMilli(),
		Status:  avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED, // Overall status is failed
		Error:   "All 2 steps failed: Manual Trigger, HTTP Request",
		Index:   0, // First execution
		Steps: []*avsproto.Execution_Step{
			{
				Id:      "trigger1",
				Success: false,
				Error:   "Trigger condition not met",
				Name:    "Manual Trigger",
			},
			{
				Id:      "step1",
				Success: false,
				Error:   "HTTP 500 error",
				Name:    "HTTP Request",
			},
		},
	}

	// Store the task and execution in the database
	taskJSON, err := task.ToJSON()
	if err != nil {
		t.Fatalf("Failed to serialize task: %v", err)
	}
	err = db.Set(TaskStorageKey(task.Id, task.Status), taskJSON)
	if err != nil {
		t.Fatalf("Failed to store task: %v", err)
	}

	// Store the execution using protojson
	executionJSON, err := protojson.Marshal(execution)
	if err != nil {
		t.Fatalf("Failed to serialize execution: %v", err)
	}
	err = db.Set(TaskExecutionKey(task, execution.Id), executionJSON)
	if err != nil {
		t.Fatalf("Failed to store execution: %v", err)
	}

	// Test GetExecutionStatus
	statusResp, err := engine.GetExecutionStatus(user, &avsproto.ExecutionReq{
		TaskId:      task.Id,
		ExecutionId: execution.Id,
	})

	if err != nil {
		t.Fatalf("GetExecutionStatus failed: %v", err)
	}

	// Verify that it returns FAILED status
	if statusResp.Status != avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED {
		t.Errorf("Expected EXECUTION_STATUS_FAILED, got %v", statusResp.Status)
	}
}
