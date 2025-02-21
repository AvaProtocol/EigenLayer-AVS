package taskengine

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AvaProtocol/ap-avs/core/testutil"
	"github.com/AvaProtocol/ap-avs/model"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
	"github.com/AvaProtocol/ap-avs/storage"
)

func TestExecutorRunTaskSucess(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	// Set up a test HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate a successful response
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status": "success"}`))
	}))
	defer server.Close()

	nodes := []*avsproto.TaskNode{
		&avsproto.TaskNode{
			Id:   "branch1",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Conditions: []*avsproto.Condition{
						&avsproto.Condition{
							Id:   "a1",
							Type: "if",
							// The test data is of this transaction https://sepolia.etherscan.io/tx/0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8 which is 3.45 token
							Expression: "Number(triggertest.data.value_formatted) >= 3",
						},
					},
				},
			},
		},
		&avsproto.TaskNode{
			Id:   "notification1",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    server.URL, // Use the test server URL
					Method: "POST",
					Body:   "hit=notification1",
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}
	edges := []*avsproto.TaskEdge{
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: trigger.Id,
			Target: "branch1",
		},
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: "branch1.a1",
			Target: "notification1",
		},
	}

	task := &model.Task{
		&avsproto.Task{
			Id:      "TaskID123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())
	execution, err := executor.RunTask(task, &QueueExecutionData{
		Reason:      testutil.GetTestEventTriggerReason(),
		ExecutionID: "exec123",
	})

	if err != nil {
		t.Errorf("expect no error but got %v", err)
	}

	if execution.TriggerName != "triggertest" {
		t.Errorf("expect field triggerName is triggertest but got: %s", execution.TriggerName)
	}

	if !execution.Success {
		t.Errorf("Expect success status but got failure")
	}

	if execution.Error != "" {
		t.Errorf("Expect no error but got: %s", execution.Error)
	}

	if len(execution.Steps) != 2 {
		t.Errorf("Expect evaluate 2 steps only but got: %d", len(execution.Steps))
	}

	if execution.Steps[0].NodeId != "branch1" {
		t.Errorf("step id doesn't match, expect branch1.a1 but got: %s", execution.Steps[0].NodeId)
	}

	if execution.Steps[0].OutputData != "branch1.a1" {
		t.Errorf("expect branch output data is `branch1.a1` but got %s", execution.Steps[0].OutputData)
	}

	if execution.Steps[1].NodeId != "notification1" {
		t.Errorf("step id doesn't match, expect notification1 but got: %s", execution.Steps[1].NodeId)
	}

	if execution.Steps[1].OutputData != "{\"status\": \"success\"}" {
		t.Errorf("expect branch output data is {\"status\": \"success\"} but got %s", execution.Steps[1].OutputData)
	}
}

func TestExecutorRunTaskStopAndReturnErrorWhenANodeFailed(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	nodes := []*avsproto.TaskNode{
		&avsproto.TaskNode{
			Id:   "branch1",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Conditions: []*avsproto.Condition{
						&avsproto.Condition{
							Id:         "a1",
							Type:       "if",
							Expression: "a >= 5",
						},
					},
				},
			},
		},
		&avsproto.TaskNode{
			Id:   "notification1",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    "https://httpbin.org/post",
					Method: "POST",
					Body:   "hit=notification1",
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}
	edges := []*avsproto.TaskEdge{
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: trigger.Id,
			Target: "branch1",
		},
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: "branch1.a1",
			Target: "notification1",
		},
	}

	task := &model.Task{
		&avsproto.Task{
			Id:      "TaskID123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())
	execution, err := executor.RunTask(task, &QueueExecutionData{
		Reason:      testutil.GetTestEventTriggerReason(),
		ExecutionID: "exec123",
	})

	if err == nil {
		t.Errorf("expect an error but the execution return no error")
	}

	if execution.Success {
		t.Errorf("Expect failure status but got success")
	}

	if len(execution.Steps) != 1 {
		t.Errorf("expect a single step but got: %d", len(execution.Steps))
	}

	if execution.Steps[0].NodeId != "branch1" {
		t.Errorf("expect evaluate branch node but got: %s", execution.Steps[0].NodeId)
	}

	if execution.Steps[0].OutputData != "" {
		t.Errorf("expect evaluate branch output data empty but got: %s", execution.Steps[0].OutputData)
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
		&avsproto.TaskNode{
			Id:   "branch1",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Conditions: []*avsproto.Condition{
						&avsproto.Condition{
							Id:         "condition1",
							Type:       "if",
							Expression: "true",
						},
					},
				},
			},
		},
		&avsproto.TaskNode{
			Id:   "rest1",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_RestApi{
				RestApi: &avsproto.RestAPINode{
					Url:    server.URL, // Use the test server URL
					Method: "POST",
					Body:   "hit=notification1",
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}
	edges := []*avsproto.TaskEdge{
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: trigger.Id,
			Target: "branch1",
		},
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: "branch1.condition1",
			Target: "rest1",
		},
	}

	task := &model.Task{
		&avsproto.Task{
			Id:      "TaskID123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())
	execution, err := executor.RunTask(task, &QueueExecutionData{
		Reason:      testutil.GetTestEventTriggerReason(),
		ExecutionID: "exec123",
	})

	if err == nil {
		t.Errorf("expected error due to 503 response but got nil")
	}

	if execution.Success {
		t.Error("Expected failure status but got success")
	}

	if len(execution.Steps) != 2 {
		t.Errorf("Expect evaluate 2 steps but got: %d", len(execution.Steps))
	}

	if execution.Steps[0].NodeId != "branch1" {
		t.Errorf("step id doesn't match, expect branch1 but got: %s", execution.Steps[0].NodeId)
	}
	if execution.Steps[1].NodeId != "rest1" {
		t.Errorf("step id doesn't match, expect branch1 but got: %s", execution.Steps[0].NodeId)
	}

}

// TestExecutorRunTaskReturnAllExecutionData to test the happy path and return all the relevant data a task needed
func TestExecutorRunTaskReturnAllExecutionData(t *testing.T) {
	SetRpc(testutil.GetTestRPCURL())
	SetCache(testutil.GetDefaultCache())
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	nodes := []*avsproto.TaskNode{
		&avsproto.TaskNode{
			Id:   "branch1",
			Name: "branch",
			TaskType: &avsproto.TaskNode_Branch{
				Branch: &avsproto.BranchNode{
					Conditions: []*avsproto.Condition{
						&avsproto.Condition{
							Id:         "condition1",
							Type:       "if",
							Expression: "Number(triggertest.data.value_formatted) >= 3",
						},
					},
				},
			},
		},
		&avsproto.TaskNode{
			Id:   "rest1",
			Name: "httpnode",
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					// Just logout the data so we can assert from the output
					Source: "JSON.stringify(triggertest.data)",
				},
			},
		},
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "triggertest",
		Name: "triggertest",
	}
	edges := []*avsproto.TaskEdge{
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: trigger.Id,
			Target: "branch1",
		},
		&avsproto.TaskEdge{
			Id:     "e1",
			Source: "branch1.condition1",
			Target: "rest1",
		},
	}

	task := &model.Task{
		&avsproto.Task{
			Id:      "TaskID123",
			Nodes:   nodes,
			Edges:   edges,
			Trigger: trigger,
		},
	}

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())
	execution, err := executor.RunTask(task, &QueueExecutionData{
		Reason:      testutil.GetTestEventTriggerReason(),
		ExecutionID: "exec123",
	})

	if execution.Id != "exec123" {
		t.Errorf("expect execution id is exec123 but got: %s", execution.Id)
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

	if execution.TriggerName != "triggertest" {
		t.Errorf("expect trigger name is triggertest but got: %s", execution.TriggerName)
	}

	reason := execution.Reason

	// cannot use deepqual here due to the pointer issue of protobuf
	if reason.BlockNumber != 7212417 {
		t.Errorf("expect BlockNumber is 7212417 but got: %d", reason.BlockNumber)
	}

	if reason.LogIndex != 98 {
		t.Errorf("expect LogIndex is 918 but got: %d", reason.LogIndex)
	}

	if reason.TxHash != "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8" {
		t.Errorf("expect TxHash is 0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8 but got: %s", reason.TxHash)
	}

	if len(execution.Steps) != 2 {
		t.Errorf("expect 2 steps but got: %d", len(execution.Steps))
	}

	if execution.Steps[0].NodeId != "branch1" {
		t.Errorf("expect branch node but got: %s", execution.Steps[0].NodeId)
	}

	outputData := execution.OutputData.(*avsproto.Execution_TransferEvent).TransferEvent

	// cannot use deepqual here due to the pointer issue of protobuf
	if outputData.TokenName != "USDC" {
		t.Errorf("expect TokenName is USDC but got: %s", outputData.TokenName)
	}

	if outputData.TokenSymbol != "USDC" {
		t.Errorf("expect TokenSymbol is AUSDC but got: %s", outputData.TokenSymbol)
	}

	if outputData.TokenDecimals != 6 {
		t.Errorf("expect TokenDecimals is 6 but got: %d", outputData.TokenDecimals)
	}

	if outputData.TransactionHash != "0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8" {
		t.Errorf("expect TransactionHash is 0x53beb2163994510e0984b436ebc828dc57e480ee671cfbe7ed52776c2a4830c8 but got: %s", outputData.TransactionHash)
	}

	if outputData.Address != "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238" {
		t.Errorf("expect Address is 0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238 but got: %s", outputData.Address)
	}

	if outputData.BlockNumber != 7212417 {
		t.Errorf("expect BlockNumber is 7212417 but got: %d", outputData.BlockNumber)
	}

	if outputData.BlockTimestamp != 1733351604 {
		t.Errorf("expect BlockTimestamp is 1733351604 but got: %d", outputData.BlockTimestamp)
	}

	if outputData.FromAddress != "0x2A6CEbeDF9e737A9C6188c62A68655919c7314DB" {
		t.Errorf("expect FromAddress is 0x2A6CEbeDF9e737A9C6188c62A68655919c7314DB but got: %s", outputData.FromAddress)
	}

	if outputData.ToAddress != "0xC114FB059434563DC65AC8D57e7976e3eaC534F4" {
		t.Errorf("expect ToAddress is 0xC114FB059434563DC65AC8D57e7976e3eaC534F4 but got: %s", outputData.ToAddress)
	}

	if outputData.Value != "3453120" {
		t.Errorf("expect Value is 3453120 but got: %s", outputData.Value)
	}

	if outputData.ValueFormatted != "3.45312" {
		t.Errorf("expect ValueFormatted is 3.45312 but got: %s", outputData.ValueFormatted)
	}

	if outputData.TransactionIndex != 73 {
		t.Errorf("expect TransactionIndex is 73 but got: %d", outputData.TransactionIndex)
	}

	fmt.Println("execution.OutputData", execution.Steps[0].Inputs, execution.Steps[1].Inputs)
}
