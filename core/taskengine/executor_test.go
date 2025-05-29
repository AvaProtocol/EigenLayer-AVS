package taskengine

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/gow"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
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
								Expression: "Number(triggertest.data.value_formatted) >= 3",
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
		return
	}

	if execution.Steps[0].NodeId != "branch1" {
		t.Errorf("step id doesn't match, expect branch1.a1 but got: %s", execution.Steps[0].NodeId)
	}

	if execution.Steps[0].GetBranch().ConditionId != "branch1.a1" {
		t.Errorf("expect branch output data is `branch1.a1` but got %s", execution.Steps[0].OutputData)
	}

	if execution.Steps[1].NodeId != "notification1" {
		t.Errorf("step id doesn't match, expect notification1 but got: %s", execution.Steps[1].NodeId)
	}

	outputData := gow.AnyToMap(execution.Steps[1].GetRestApi().Data)
	bodyData := outputData["body"].(map[string]interface{})
	if bodyData["message"].(string) != "I'm hit" {
		t.Errorf("expect message to be 'I'm hit' but got %s", bodyData["message"])
	}
}

func TestExecutorRunTaskStopAndReturnErrorWhenANodeFailed(t *testing.T) {
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
								Id:         "a1",
								Type:       "if",
								Expression: "a >= 5",
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
						Url:    "https://httpbin.org/post",
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

	if execution.Steps[0].GetBranch() != nil {
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
								Expression: "Number(triggertest.data.value_formatted) >= 3",
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

	if len(execution.Steps) != 4 {
		t.Errorf("expect 4 steps but got: %d", len(execution.Steps))
	}

	outputData := execution.OutputData.(*avsproto.Execution_EventTrigger).EventTrigger.TransferLog

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

	if outputData.BlockTimestamp != 1733351604000 {
		t.Errorf("expect BlockTimestamp is 1733351604000 but got: %d", outputData.BlockTimestamp)
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

	if !reflect.DeepEqual([]string{"spacex", "branch1", "customcode1", "rest1"}, []string{execution.Steps[0].NodeId, execution.Steps[1].NodeId, execution.Steps[2].NodeId, execution.Steps[3].NodeId}) {
		t.Errorf("expect nodeid as spacex, branch1, customcode1, rest1 but got: %s", []string{execution.Steps[0].NodeId, execution.Steps[1].NodeId, execution.Steps[2].NodeId, execution.Steps[3].NodeId})
	}

	// Verify the inputs of each step
	expectedInputsStep0 := []string{"apContext.configVars", "spacex.data", "triggertest.data"}
	expectedInputsStep1 := []string{"apContext.configVars", "spacex.data", "triggertest.data"}
	expectedInputsStep2 := []string{"apContext.configVars", "dummy.data", "spacex.data", "triggertest.data"}
	expectedInputsStep3 := []string{"apContext.configVars", "dummy.data", "http.data", "spacex.data", "triggertest.data"}

	// Sort the expected and actual inputs before comparison
	sort.Strings(expectedInputsStep0)
	sort.Strings(execution.Steps[0].Inputs)
	if !reflect.DeepEqual(execution.Steps[0].Inputs, expectedInputsStep0) {
		t.Errorf("expect inputs for step 0 to be %v but got: %v", expectedInputsStep0, execution.Steps[0].Inputs)
	}

	sort.Strings(expectedInputsStep1)
	sort.Strings(execution.Steps[1].Inputs)
	if !reflect.DeepEqual(execution.Steps[1].Inputs, expectedInputsStep1) {
		t.Errorf("expect inputs for step 1 to be %v but got: %v", expectedInputsStep1, execution.Steps[1].Inputs)
	}

	sort.Strings(expectedInputsStep2)
	sort.Strings(execution.Steps[2].Inputs)
	if !reflect.DeepEqual(execution.Steps[2].Inputs, expectedInputsStep2) {
		t.Errorf("expect inputs for step 2 to be %v but got: %v", expectedInputsStep2, execution.Steps[2].Inputs)
	}

	sort.Strings(expectedInputsStep3)
	sort.Strings(execution.Steps[3].Inputs)
	if !reflect.DeepEqual(execution.Steps[3].Inputs, expectedInputsStep3) {
		t.Errorf("expect inputs for step 3 to be %v but got: %v", expectedInputsStep3, execution.Steps[3].Inputs)
	}
}

func TestExecutorRunTaskWithBlockTriggerOutputData(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	// Create a simple task with block trigger and ETH transfer node
	nodes := []*avsproto.TaskNode{
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

	// Create block trigger reason
	blockTriggerReason := &avsproto.TriggerReason{
		Type:        avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		BlockNumber: 8416691, // Same block number as in SDK test
	}

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())
	execution, err := executor.RunTask(task, &QueueExecutionData{
		Reason:      blockTriggerReason,
		ExecutionID: "block_exec123",
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

	if execution.TriggerName != "blockTrigger" {
		t.Errorf("expect trigger name is blockTrigger but got: %s", execution.TriggerName)
	}

	// Check trigger reason
	reason := execution.Reason
	if reason.BlockNumber != 8416691 {
		t.Errorf("expect BlockNumber is 8416691 but got: %d", reason.BlockNumber)
	}

	if reason.Type != avsproto.TriggerType_TRIGGER_TYPE_BLOCK {
		t.Errorf("expect trigger type is Block but got: %v", reason.Type)
	}

	// **This is the key test that was missing - check block trigger output data**
	if execution.OutputData == nil {
		t.Fatal("expect OutputData to be set but got nil")
	}

	blockTriggerOutput, ok := execution.OutputData.(*avsproto.Execution_BlockTrigger)
	if !ok {
		t.Fatalf("expect OutputData to be BlockTrigger type but got: %T", execution.OutputData)
	}

	if blockTriggerOutput.BlockTrigger == nil {
		t.Fatal("expect BlockTrigger output data to be set but got nil")
	}

	if blockTriggerOutput.BlockTrigger.BlockNumber != 8416691 {
		t.Errorf("expect BlockTrigger.BlockNumber is 8416691 but got: %d", blockTriggerOutput.BlockTrigger.BlockNumber)
	}

	// Verify execution steps
	if len(execution.Steps) != 1 {
		t.Errorf("expect 1 step but got: %d", len(execution.Steps))
	}

	if execution.Steps[0].NodeId != "eth_transfer_1" {
		t.Errorf("expect step NodeId is eth_transfer_1 but got: %s", execution.Steps[0].NodeId)
	}

	if !execution.Steps[0].Success {
		t.Errorf("expect step to succeed but got failure: %s", execution.Steps[0].Error)
	}
}

func TestExecutorRunTaskWithFixedTimeTriggerOutputData(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	// Create a simple task with fixed time trigger and ETH transfer node
	nodes := []*avsproto.TaskNode{
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

	// Create fixed time trigger reason
	fixedTimeTriggerReason := &avsproto.TriggerReason{
		Type:  avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME,
		Epoch: 1640995200, // Same epoch as in trigger config
	}

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())
	execution, err := executor.RunTask(task, &QueueExecutionData{
		Reason:      fixedTimeTriggerReason,
		ExecutionID: "fixed_time_exec123",
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

	if execution.TriggerName != "fixedTimeTrigger" {
		t.Errorf("expect trigger name is fixedTimeTrigger but got: %s", execution.TriggerName)
	}

	// Check trigger reason
	reason := execution.Reason
	if reason.Epoch != 1640995200 {
		t.Errorf("expect Epoch is 1640995200 but got: %d", reason.Epoch)
	}

	if reason.Type != avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME {
		t.Errorf("expect trigger type is FixedTime but got: %v", reason.Type)
	}

	// **Check fixed time trigger output data**
	if execution.OutputData == nil {
		t.Fatal("expect OutputData to be set but got nil")
	}

	fixedTimeTriggerOutput, ok := execution.OutputData.(*avsproto.Execution_FixedTimeTrigger)
	if !ok {
		t.Fatalf("expect OutputData to be FixedTimeTrigger type but got: %T", execution.OutputData)
	}

	if fixedTimeTriggerOutput.FixedTimeTrigger == nil {
		t.Fatal("expect FixedTimeTrigger output data to be set but got nil")
	}

	if fixedTimeTriggerOutput.FixedTimeTrigger.Epoch != 1640995200 {
		t.Errorf("expect FixedTimeTrigger.Epoch is 1640995200 but got: %d", fixedTimeTriggerOutput.FixedTimeTrigger.Epoch)
	}
}

func TestExecutorRunTaskWithCronTriggerOutputData(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	// Create a simple task with cron trigger and ETH transfer node
	nodes := []*avsproto.TaskNode{
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
	}

	trigger := &avsproto.TaskTrigger{
		Id:   "cron_trigger",
		Name: "cronTrigger",
		TriggerType: &avsproto.TaskTrigger_Cron{
			Cron: &avsproto.CronTrigger{
				Config: &avsproto.CronTrigger_Config{
					Schedule: []string{"0 0 * * *"}, // Daily at midnight
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

	// Create cron trigger reason
	cronTriggerReason := &avsproto.TriggerReason{
		Type:  avsproto.TriggerType_TRIGGER_TYPE_CRON,
		Epoch: 1640995200, // Epoch when cron was triggered
	}

	executor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())
	execution, err := executor.RunTask(task, &QueueExecutionData{
		Reason:      cronTriggerReason,
		ExecutionID: "cron_exec123",
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

	if execution.TriggerName != "cronTrigger" {
		t.Errorf("expect trigger name is cronTrigger but got: %s", execution.TriggerName)
	}

	// Check trigger reason
	reason := execution.Reason
	if reason.Epoch != 1640995200 {
		t.Errorf("expect Epoch is 1640995200 but got: %d", reason.Epoch)
	}

	if reason.Type != avsproto.TriggerType_TRIGGER_TYPE_CRON {
		t.Errorf("expect trigger type is Cron but got: %v", reason.Type)
	}

	// **Check cron trigger output data**
	if execution.OutputData == nil {
		t.Fatal("expect OutputData to be set but got nil")
	}

	cronTriggerOutput, ok := execution.OutputData.(*avsproto.Execution_CronTrigger)
	if !ok {
		t.Fatalf("expect OutputData to be CronTrigger type but got: %T", execution.OutputData)
	}

	if cronTriggerOutput.CronTrigger == nil {
		t.Fatal("expect CronTrigger output data to be set but got nil")
	}

	if cronTriggerOutput.CronTrigger.Epoch != 1640995200 {
		t.Errorf("expect CronTrigger.Epoch is 1640995200 but got: %d", cronTriggerOutput.CronTrigger.Epoch)
	}

	// ScheduleMatched should be empty since it's not available in TriggerReason
	if cronTriggerOutput.CronTrigger.ScheduleMatched != "" {
		t.Errorf("expect CronTrigger.ScheduleMatched to be empty but got: %s", cronTriggerOutput.CronTrigger.ScheduleMatched)
	}
}
