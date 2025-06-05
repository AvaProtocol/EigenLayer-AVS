package taskengine

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/apqueue"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/gow"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/require"
)

func TestCreateTaskReturnErrorWhenEmptyNodes(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	tr := testutil.RestTask()
	tr.Name = "t1"
	tr.SmartWalletAddress = "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"
	tr.Nodes = []*avsproto.TaskNode{}
	//tr.Edges = []*avsproto.Edge{}
	_, err := n.CreateTask(testutil.TestUser1(), tr)
	if err == nil {
		t.Errorf("expect error when create task with empty nodes or edges")
	}

	if err.Error() != "rpc error: code = InvalidArgument desc = invalid: nodes field cannot be an empty array" {
		t.Errorf("expect error `code = InvalidArgument desc = invalid: nodes field cannot be an empty array`, got %s", err.Error())
	}
}

func TestCreateTaskReturnErrorWhenEmptyEdges(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	tr := testutil.RestTask()
	tr.Name = "t1"
	tr.SmartWalletAddress = "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"
	tr.Edges = []*avsproto.TaskEdge{}
	_, err := n.CreateTask(testutil.TestUser1(), tr)
	if err.Error() != "rpc error: code = InvalidArgument desc = invalid: edges field cannot be an empty array" {
		t.Errorf("expect error `code = InvalidArgument desc = invalid: edges field cannot be an empty array`, got %s", err.Error())
	}
}

func TestCreateTaskReturnErrorWhenInvalidBlockTriggerInterval(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	tests := []struct {
		name     string
		interval int64
		wantErr  bool
	}{
		{
			name:     "zero interval should fail",
			interval: 0,
			wantErr:  true,
		},
		{
			name:     "negative interval should fail",
			interval: -1,
			wantErr:  true,
		},
		{
			name:     "positive interval should succeed",
			interval: 10,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := testutil.RestTask()
			tr.Name = "test-task-" + tt.name
			tr.SmartWalletAddress = "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"

			// Set the block trigger interval
			tr.Trigger.GetBlock().GetConfig().Interval = tt.interval

			_, err := n.CreateTask(testutil.TestUser1(), tr)

			if tt.wantErr && err == nil {
				t.Errorf("CreateTask() expected error for interval %d, but got none", tt.interval)
			}

			if !tt.wantErr && err != nil {
				t.Errorf("CreateTask() unexpected error for interval %d: %v", tt.interval, err)
			}

			if tt.wantErr && err != nil {
				t.Logf("CreateTask() correctly rejected interval %d with error: %v", tt.interval, err)
				// Verify the error message indicates invalid task argument
				if !strings.Contains(err.Error(), "Invalid task argument") {
					t.Errorf("Expected error to contain 'Invalid task argument', got: %v", err)
				}
			}
		})
	}
}

func TestListTasks(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	n.GetWallet(testutil.TestUser1(), &avsproto.GetWalletReq{
		Salt: "12345",
	})
	n.GetWallet(testutil.TestUser1(), &avsproto.GetWalletReq{
		Salt: "6789",
	})

	// Now create a test task
	tr1 := testutil.RestTask()
	tr1.Name = "t1"
	// salt 0
	tr1.SmartWalletAddress = "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"
	n.CreateTask(testutil.TestUser1(), tr1)

	tr2 := testutil.RestTask()
	tr2.Name = "t2"
	// salt 12345
	tr2.SmartWalletAddress = "0x961d2DD008960A9777571D78D21Ec9C3E5c6020c"
	n.CreateTask(testutil.TestUser1(), tr2)

	tr3 := testutil.RestTask()
	// salt 6789
	tr3.Name = "t3"
	tr3.SmartWalletAddress = "0x5D36dCdB35D0C85D88C5AA31E37cac165B480ba4"
	n.CreateTask(testutil.TestUser1(), tr3)

	result, err := n.ListTasksByUser(testutil.TestUser1(), &avsproto.ListTasksReq{
		SmartWalletAddress: []string{"0x5D36dCdB35D0C85D88C5AA31E37cac165B480ba4"},
	})

	if err != nil {
		t.Errorf("expect list task successfully but got error %s", err)
		return
	}

	if result == nil {
		t.Errorf("expect result is not nil but got nil")
		return
	}

	if len(result.Items) != 1 {
		t.Errorf("list task return wrong. expect 1, got %d", len(result.Items))
		return
	}

	if result.Items[0].Name != "t3" {
		t.Errorf("list task return wrong. expect memo t1, got %s", result.Items[0].Name)
	}

	result, err = n.ListTasksByUser(testutil.TestUser1(), &avsproto.ListTasksReq{
		SmartWalletAddress: []string{
			"0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6",
			"0x961d2DD008960A9777571D78D21Ec9C3E5c6020c",
		},
	})

	if len(result.Items) != 2 {
		t.Errorf("list task returns wrong. expect 2, got %d", len(result.Items))
	}
	if result.Items[0].Name != "t2" && result.Items[1].Name != "t1" {
		t.Errorf("list task returns wrong data. expect t2, t1 got %s, %s", result.Items[0].Name, result.Items[1].Name)
	}
}

func TestListTasksPagination(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	n.GetWallet(testutil.TestUser1(), &avsproto.GetWalletReq{
		Salt: "12345",
	})
	n.GetWallet(testutil.TestUser1(), &avsproto.GetWalletReq{
		Salt: "6789",
	})

	// Firs we setup test for a 3 smart walets, with overlap ordering
	// Now create a test task
	tr1 := testutil.RestTask()
	tr1.Name = "t1"
	// salt 0
	tr1.SmartWalletAddress = "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"
	n.CreateTask(testutil.TestUser1(), tr1)

	tr2 := testutil.RestTask()
	tr2.Name = "t2_1"
	// salt 12345
	tr2.SmartWalletAddress = "0x961d2DD008960A9777571D78D21Ec9C3E5c6020c"
	n.CreateTask(testutil.TestUser1(), tr2)

	for i := 0; i < 20; i++ {
		tr3 := testutil.RestTask()
		// salt 6789
		tr3.Name = fmt.Sprintf("t3_%d", i)
		tr3.SmartWalletAddress = "0x5D36dCdB35D0C85D88C5AA31E37cac165B480ba4"
		n.CreateTask(testutil.TestUser1(), tr3)
	}

	tr2 = testutil.RestTask()
	tr2.Name = "t2_2"
	// salt 12345
	tr2.SmartWalletAddress = "0x961d2DD008960A9777571D78D21Ec9C3E5c6020c"
	n.CreateTask(testutil.TestUser1(), tr2)

	// Now we start to list task of a list of smart wallet, assert that result doesn't contains tasks of other wallet, ordering and pagination follow cursor should return right data too
	result, err := n.ListTasksByUser(testutil.TestUser1(), &avsproto.ListTasksReq{
		SmartWalletAddress: []string{
			"0x961d2DD008960A9777571D78D21Ec9C3E5c6020c",
			"0x5D36dCdB35D0C85D88C5AA31E37cac165B480ba4",
		},
		Limit: 5,
	})

	if err != nil {
		t.Errorf("expect list task successfully but got error %s", err)
	}

	if !result.PageInfo.HasNextPage {
		t.Errorf("expect hasmore is true, but got false")
	}

	if len(result.Items) != 5 {
		t.Errorf("list task returns wrong. expect 5, got %d", len(result.Items))
	}
	if result.Items[0].Name != "t2_2" {
		t.Errorf("list task returns first task wrong. expect task t2, got %s", result.Items[0].Name)
	}

	if result.Items[2].Name != "t3_18" || result.Items[4].Name != "t3_16" {
		t.Errorf("list task returns wrong task result, expected t3_19 t3_17 got %s %s", result.Items[2].Name, result.Items[4].Name)
	}

	if result.PageInfo.EndCursor == "" {
		t.Errorf("list task returns wrong cursor. expect non empty, got none")
	}
	result, err = n.ListTasksByUser(testutil.TestUser1(), &avsproto.ListTasksReq{
		SmartWalletAddress: []string{
			"0x961d2DD008960A9777571D78D21Ec9C3E5c6020c",
			"0x5D36dCdB35D0C85D88C5AA31E37cac165B480ba4",
		},
		Limit: 15,
		After: result.PageInfo.EndCursor,
	})

	if len(result.Items) != 15 {
		t.Errorf("list task returns wrong. expect 15, got %d", len(result.Items))
	}
	if result.Items[0].Name != "t3_15" || result.Items[2].Name != "t3_13" || result.Items[14].Name != "t3_1" {
		t.Errorf("list task returns wrong task result, expected t3_15 t3_13 t3_1 got %s %s %s", result.Items[0].Name, result.Items[2].Name, result.Items[14].Name)
	}

	if !result.PageInfo.HasNextPage {
		t.Errorf("expect hasmore is true, but got false")
	}

	result, err = n.ListTasksByUser(testutil.TestUser1(), &avsproto.ListTasksReq{
		SmartWalletAddress: []string{
			"0x961d2DD008960A9777571D78D21Ec9C3E5c6020c",
			"0x5D36dCdB35D0C85D88C5AA31E37cac165B480ba4",
		},
		Limit: 15,
		After: result.PageInfo.EndCursor,
	})

	if len(result.Items) != 2 {
		t.Errorf("list task returns wrong. expect 2, got %d", len(result.Items))
	}
	if result.Items[0].Name != "t3_0" || result.Items[1].Name != "t2_1" {
		t.Errorf("list task returns wrong task result, expected t3_15 t3_1 got %s %s", result.Items[0].Name, result.Items[1].Name)
	}
	if result.PageInfo.HasNextPage {
		t.Errorf("expect hasmore is false, but got true")
	}

}

func TestGetExecution(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	// Now create a test task
	tr1 := testutil.RestTask()
	tr1.Name = "t1"
	// salt 0
	tr1.SmartWalletAddress = "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"
	result, _ := n.CreateTask(testutil.TestUser1(), tr1)

	resultTrigger, err := n.TriggerTask(testutil.TestUser1(), &avsproto.TriggerTaskReq{
		TaskId:      result.Id,
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		TriggerOutput: &avsproto.TriggerTaskReq_BlockTrigger{
			BlockTrigger: &avsproto.BlockTrigger_Output{
				BlockNumber: uint64(101),
			},
		},
		IsBlocking: true,
	})

	if err != nil {
		t.Errorf("failed to trigger task: %v", err)
		return
	}

	if resultTrigger == nil {
		t.Errorf("resultTrigger is nil")
		return
	}

	// Now get back that execution data using the log
	execution, err := n.GetExecution(testutil.TestUser1(), &avsproto.ExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})

	if err != nil {
		t.Errorf("failed to get execution: %v", err)
		return
	}

	if execution == nil {
		t.Errorf("execution is nil")
		return
	}

	// Regular workflow executions now contain both trigger and node steps
	// Step 0: Trigger step, Step 1: Node step
	if len(execution.Steps) != 2 {
		t.Errorf("Expected 2 steps (trigger + node), but got %d", len(execution.Steps))
		return
	}

	// Verify the first step is the trigger step
	triggerStep := execution.Steps[0]
	if triggerStep.Type != avsproto.TriggerType_TRIGGER_TYPE_BLOCK.String() {
		t.Errorf("First step should be trigger step, but got type: %s", triggerStep.Type)
	}

	// The second step should be the node step
	if execution.Steps[1].Id != "ping1" {
		t.Errorf("wrong node id in execution log, expected ping1 but got %s", execution.Steps[1].Id)
	}

	step := execution.Steps[1]
	if step.GetRestApi() == nil {
		t.Errorf("RestApi data is nil")
		return
	}

	// Get the response data as a map
	responseData := gow.ValueToMap(step.GetRestApi().Data)
	if responseData == nil {
		t.Errorf("Failed to convert response data to map")
		return
	}

	// Check if the response body contains "httpbin.org"
	// The response structure might have changed, so let's handle both string and map cases
	var bodyContent string
	if bodyStr, ok := responseData["body"].(string); ok {
		bodyContent = bodyStr
	} else if bodyMap, ok := responseData["body"].(map[string]interface{}); ok {
		// If body is a map, convert it to string for checking
		if bodyBytes, err := json.Marshal(bodyMap); err == nil {
			bodyContent = string(bodyBytes)
		} else {
			t.Errorf("Failed to marshal body map to string: %v", err)
			return
		}
	} else {
		t.Errorf("Response body is neither string nor map, got type: %T", responseData["body"])
		return
	}

	if !strings.Contains(bodyContent, "httpbin.org") {
		maxLen := 100
		if len(bodyContent) < maxLen {
			maxLen = len(bodyContent)
		}
		t.Errorf("Invalid output data. Expected body to contain 'httpbin.org' but got: %s", bodyContent[:maxLen]+"...")
	}

	// If we get the status back it also reflected
	executionStatus, err := n.GetExecutionStatus(testutil.TestUser1(), &avsproto.ExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})
	if err != nil {
		t.Fatalf("Error getting execution status after processing: %v", err)
	}

	if executionStatus.Status != avsproto.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		t.Errorf("expected status to be completed but got %v", executionStatus.Status)
	}

	// Note: TaskTriggerKey cleanup is only relevant for asynchronous execution
	// This test uses synchronous execution (IsBlocking: true), so we don't check for cleanup
}

func TestListWallets(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	u := testutil.TestUser1()

	n.GetWallet(u, &avsproto.GetWalletReq{
		Salt: "12345",
	})
	n.GetWallet(u, &avsproto.GetWalletReq{
		Salt: "9876",
		// https://sepolia.etherscan.io/address/0x9406Cc6185a346906296840746125a0E44976454#readProxyContract
		FactoryAddress: "0x9406Cc6185a346906296840746125a0E44976454",
	})

	resp, _ := n.ListWallets(u.Address, nil)
	if len(resp.Items) != 3 {
		t.Errorf("expect 3 smartwallets but got %d", len(resp.Items))
	}

	// The default wallet with salt 0
	if resp.Items[0].Address != "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6" {
		t.Errorf("invalid smartwallet address, expect 0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6 got %s", resp.Items[0].Address)
	}

	// This is the wallet from custom factory https://sepolia.etherscan.io/address/0x9406Cc6185a346906296840746125a0E44976454#readProxyContract
	if resp.Items[1].Address != "0x29C3139e460d03d951070596eED3218B3cc34FD1" {
		t.Errorf("invalid smartwallet address, expect 0x923A6A90E422871FC56020d560Bc0D0CF1fbb93e got %s", resp.Items[1].Address)
	}

	// the wallet with default factory and salt 12345
	if resp.Items[2].Address != "0x961d2DD008960A9777571D78D21Ec9C3E5c6020c" {
		t.Errorf("invalid smartwallet address, expect 0x961d2DD008960A9777571D78D21Ec9C3E5c6020c got %s", resp.Items[2].Address)
	}

	resp, _ = n.ListWallets(u.Address, &avsproto.ListWalletReq{
		FactoryAddress: "0x9406Cc6185a346906296840746125a0E44976454",
	})
	if len(resp.Items) != 1 {
		t.Errorf("expect 1 smartwallet but got %d", len(resp.Items))
	}
	// owner 0xD7050816337a3f8f690F8083B5Ff8019D50c0E50 salt 0 https://sepolia.etherscan.io/address/0x29adA1b5217242DEaBB142BC3b1bCfFdd56008e7#readContract
	if resp.Items[0].Address != "0x29C3139e460d03d951070596eED3218B3cc34FD1" {
		t.Errorf("invalid smartwallet address, expect 0x29C3139e460d03d951070596eED3218B3cc34FD1 got %s", resp.Items[0].Address)
	}

	if resp.Items[0].Salt != "9876" {
		t.Errorf("invalid smartwallet address salt, expect 9876 got %s", resp.Items[0].Salt)
	}

	// other user will not be able to list above wallet
	resp, _ = n.ListWallets(testutil.TestUser2().Address, nil)
	if len(resp.Items) != 1 {
		t.Errorf("expect 1 wallets but got %d", len(resp.Items))
	}
}

func TestTriggerSync(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	// Now create a test task
	tr1 := testutil.RestTask()
	tr1.Name = "t1"
	// salt 0
	tr1.SmartWalletAddress = "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"
	result, _ := n.CreateTask(testutil.TestUser1(), tr1)

	resultTrigger, err := n.TriggerTask(testutil.TestUser1(), &avsproto.TriggerTaskReq{
		TaskId:      result.Id,
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		TriggerOutput: &avsproto.TriggerTaskReq_BlockTrigger{
			BlockTrigger: &avsproto.BlockTrigger_Output{
				BlockNumber: uint64(101),
			},
		},
		IsBlocking: true,
	})

	if err != nil {
		t.Errorf("expected first trigger to succeed but got error: %v", err)
		return
	}

	if resultTrigger == nil {
		t.Errorf("resultTrigger is nil")
		return
	}

	// Now get back that execution id
	execution, err := n.GetExecution(testutil.TestUser1(), &avsproto.ExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})

	if err != nil {
		t.Errorf("failed to get execution: %v", err)
		return
	}

	if execution == nil {
		t.Errorf("execution is nil")
		return
	}

	// Regular workflow executions now contain both trigger and node steps
	// Step 0: Trigger step, Step 1: Node step
	if len(execution.Steps) != 2 {
		t.Errorf("Expected 2 steps (trigger + node), but got %d", len(execution.Steps))
		return
	}

	// Verify the first step is the trigger step
	triggerStep := execution.Steps[0]
	if triggerStep.Type != avsproto.TriggerType_TRIGGER_TYPE_BLOCK.String() {
		t.Errorf("First step should be trigger step, but got type: %s", triggerStep.Type)
	}

	// The second step should be the node step
	if execution.Steps[1].Id != "ping1" {
		t.Errorf("wrong node id in execution log, expected ping1 but got %s", execution.Steps[1].Id)
	}

	step := execution.Steps[1]
	if step.GetRestApi() == nil {
		t.Errorf("RestApi data is nil")
		return
	}

	// Get the response data as a map
	responseData := gow.ValueToMap(step.GetRestApi().Data)
	if responseData == nil {
		t.Errorf("Failed to convert response data to map")
		return
	}

	// Check if the response body contains "httpbin.org"
	// The response structure might have changed, so let's handle both string and map cases
	var bodyContent string
	if bodyStr, ok := responseData["body"].(string); ok {
		bodyContent = bodyStr
	} else if bodyMap, ok := responseData["body"].(map[string]interface{}); ok {
		// If body is a map, convert it to string for checking
		if bodyBytes, err := json.Marshal(bodyMap); err == nil {
			bodyContent = string(bodyBytes)
		} else {
			t.Errorf("Failed to marshal body map to string: %v", err)
			return
		}
	} else {
		t.Errorf("Response body is neither string nor map, got type: %T", responseData["body"])
		return
	}

	if !strings.Contains(bodyContent, "httpbin.org") {
		maxLen := 100
		if len(bodyContent) < maxLen {
			maxLen = len(bodyContent)
		}
		t.Errorf("Invalid output data. Expected body to contain 'httpbin.org' but got: %s", bodyContent[:maxLen]+"...")
	}

	// If we get the status back it also reflected
	executionStatus, err := n.GetExecutionStatus(testutil.TestUser1(), &avsproto.ExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})
	if err != nil {
		t.Fatalf("Error getting execution status after processing: %v", err)
	}

	if executionStatus.Status != avsproto.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		t.Errorf("expected status to be completed but got %v", executionStatus.Status)
	}

	// Note: TaskTriggerKey cleanup is only relevant for asynchronous execution
	// This test uses synchronous execution (IsBlocking: true), so we don't check for cleanup
}

func TestTriggerAsync(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	n.queue = apqueue.New(db, testutil.GetLogger(), &apqueue.QueueOption{
		Prefix: "default",
	})
	worker := apqueue.NewWorker(n.queue, n.db)
	taskExecutor := NewExecutor(testutil.GetTestSmartWalletConfig(), db, testutil.GetLogger())
	worker.RegisterProcessor(
		JobTypeExecuteTask,
		taskExecutor,
	)
	n.queue.MustStart()

	// Now create a test task
	tr1 := testutil.RestTask()
	tr1.Name = "t1"
	// salt 0 wallet
	tr1.SmartWalletAddress = "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"
	result, _ := n.CreateTask(testutil.TestUser1(), tr1)

	resultTrigger, err := n.TriggerTask(testutil.TestUser1(), &avsproto.TriggerTaskReq{
		TaskId:      result.Id,
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		TriggerOutput: &avsproto.TriggerTaskReq_BlockTrigger{
			BlockTrigger: &avsproto.BlockTrigger_Output{
				BlockNumber: uint64(101),
			},
		},
		IsBlocking: false,
	})

	if err != nil {
		t.Errorf("expected first trigger to succeed but got error: %v", err)
	}

	// Now get back that execution id, because the task is run async we won't have any data yet,
	// just the status for now
	executionStatus, err := n.GetExecutionStatus(testutil.TestUser1(), &avsproto.ExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})

	if executionStatus.Status != avsproto.ExecutionStatus_EXECUTION_STATUS_PENDING {
		t.Errorf("expected status to be pending but got %v", executionStatus.Status)
	}

	// Now let the queue start and process job
	// In our end to end system the worker will process the job eventually
	worker.ProcessSignal(1)

	execution, err := n.GetExecution(testutil.TestUser1(), &avsproto.ExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})

	if !execution.Success {
		t.Errorf("wrong success result, expected true got false. Error: %s", execution.Error)
	}

	// Now that trigger steps are included, we should have at least 2 steps (trigger + node)
	if len(execution.Steps) != 2 {
		t.Errorf("Expected 2 steps (trigger + node), but got %d", len(execution.Steps))
		return
	}

	// Verify the first step is the trigger step
	if execution.Steps[0].Type != avsproto.TriggerType_TRIGGER_TYPE_BLOCK.String() {
		t.Errorf("First step should be trigger step, but got: %s", execution.Steps[0].Type)
	}

	if execution.Steps[1].Id != "ping1" {
		t.Errorf("wrong node id in execution log, expected ping1 but got %s", execution.Steps[1].Id)
	}

	// Debug: Print step information
	for i, step := range execution.Steps {
		t.Logf("Step %d: ID=%s, Type=%s, Name=%s", i, step.Id, step.Type, step.Name)
	}

	step := execution.Steps[1]
	if step.GetRestApi() == nil {
		t.Errorf("RestApi data is nil")
		return
	}

	// Get the response data as a map
	responseData := gow.ValueToMap(step.GetRestApi().Data)
	if responseData == nil {
		t.Errorf("Failed to convert response data to map")
		return
	}

	// Check if the response body contains "httpbin.org"
	// The response structure might have changed, so let's handle both string and map cases
	var bodyContent string
	if bodyStr, ok := responseData["body"].(string); ok {
		bodyContent = bodyStr
	} else if bodyMap, ok := responseData["body"].(map[string]interface{}); ok {
		// If body is a map, convert it to string for checking
		if bodyBytes, err := json.Marshal(bodyMap); err == nil {
			bodyContent = string(bodyBytes)
		} else {
			t.Errorf("Failed to marshal body map to string: %v", err)
			return
		}
	} else {
		t.Errorf("Response body is neither string nor map, got type: %T", responseData["body"])
		return
	}

	if !strings.Contains(bodyContent, "httpbin.org") {
		maxLen := 100
		if len(bodyContent) < maxLen {
			maxLen = len(bodyContent)
		}
		t.Errorf("Invalid output data. Expected body to contain 'httpbin.org' but got: %s", bodyContent[:maxLen]+"...")
	}

	// If we get the status back it also reflected
	executionStatus, err = n.GetExecutionStatus(testutil.TestUser1(), &avsproto.ExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})
	if err != nil {
		t.Fatalf("Error getting execution status after processing: %v", err)
	}

	if executionStatus.Status != avsproto.ExecutionStatus_EXECUTION_STATUS_COMPLETED {
		t.Errorf("expected status to be completed but got %v", executionStatus.Status)
	}

	// Note: TaskTriggerKey cleanup is only relevant for asynchronous execution
	// This test uses synchronous execution (IsBlocking: false), so we don't check for cleanup
}

func TestTriggerCompletedTaskReturnError(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	// Now create a test task
	tr1 := testutil.RestTask()
	tr1.Name = "t1"
	tr1.MaxExecution = 1
	// salt 0
	tr1.SmartWalletAddress = "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"
	result, _ := n.CreateTask(testutil.TestUser1(), tr1)

	resultTrigger, err := n.TriggerTask(testutil.TestUser1(), &avsproto.TriggerTaskReq{
		TaskId:      result.Id,
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		TriggerOutput: &avsproto.TriggerTaskReq_BlockTrigger{
			BlockTrigger: &avsproto.BlockTrigger_Output{
				BlockNumber: uint64(101),
			},
		},
		IsBlocking: true,
	})

	if err != nil || resultTrigger == nil {
		t.Errorf("expected first trigger to succeed but got error: %v", err)
		return
	}

	// Now the task has reach its max run, and canot run anymore
	resultTrigger, err = n.TriggerTask(testutil.TestUser1(), &avsproto.TriggerTaskReq{
		TaskId:      result.Id,
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		TriggerOutput: &avsproto.TriggerTaskReq_BlockTrigger{
			BlockTrigger: &avsproto.BlockTrigger_Output{
				BlockNumber: uint64(101),
			},
		},
		IsBlocking: true,
	})

	if err == nil || resultTrigger != nil {
		t.Errorf("expect trigger error but succeed")
	}
}

// TestBuildEventTriggerOutputDefensiveProgramming tests that buildEventTriggerOutput
// gracefully handles nil and unexpected inputs without causing runtime errors.
func TestBuildEventTriggerOutputDefensiveProgramming(t *testing.T) {
	tests := []struct {
		name           string
		input          map[string]interface{}
		expectedResult bool // true if we expect a valid output
		description    string
	}{
		{
			name:           "Nil input",
			input:          nil,
			expectedResult: true,
			description:    "Should return empty EventTrigger_Output for nil input",
		},
		{
			name:           "Empty map",
			input:          map[string]interface{}{},
			expectedResult: true,
			description:    "Should return empty EventTrigger_Output for empty map",
		},
		{
			name: "Invalid types for all fields",
			input: map[string]interface{}{
				"found":        "not_a_bool",
				"transfer_log": "not_a_map",
				"evm_log":      12345,
			},
			expectedResult: true,
			description:    "Should handle invalid types gracefully",
		},
		{
			name: "Found=false",
			input: map[string]interface{}{
				"found": false,
			},
			expectedResult: true,
			description:    "Should return empty output when found=false",
		},
		{
			name: "Found=true but no event data",
			input: map[string]interface{}{
				"found": true,
			},
			expectedResult: true,
			description:    "Should return empty output when found=true but no event data",
		},
		{
			name: "Found=true with invalid transfer_log",
			input: map[string]interface{}{
				"found":        true,
				"transfer_log": "invalid_type",
			},
			expectedResult: true,
			description:    "Should handle invalid transfer_log type gracefully",
		},
		{
			name: "Found=true with valid transfer_log structure",
			input: map[string]interface{}{
				"found": true,
				"transfer_log": map[string]interface{}{
					"tokenName":        "TestToken",
					"tokenSymbol":      "TEST",
					"tokenDecimals":    uint32(18),
					"transactionHash":  "0x123",
					"address":          "0x456",
					"blockNumber":      uint64(12345),
					"blockTimestamp":   uint64(1234567890),
					"fromAddress":      "0x789",
					"toAddress":        "0xabc",
					"value":            "1000000000000000000",
					"valueFormatted":   "1.0",
					"transactionIndex": uint32(5),
					"logIndex":         uint32(2),
				},
			},
			expectedResult: true,
			description:    "Should properly parse valid transfer_log data",
		},
		{
			name: "Found=true with valid evm_log structure",
			input: map[string]interface{}{
				"found": true,
				"evm_log": map[string]interface{}{
					"address":          "0x123",
					"topics":           []string{"0xtopic1", "0xtopic2"},
					"data":             "0xdata",
					"blockNumber":      uint64(12345),
					"transactionHash":  "0x456",
					"transactionIndex": uint32(3),
					"blockHash":        "0x789",
					"index":            uint32(1),
					"removed":          false,
				},
			},
			expectedResult: true,
			description:    "Should properly parse valid evm_log data",
		},
		{
			name: "Found=true with transfer_log containing wrong types",
			input: map[string]interface{}{
				"found": true,
				"transfer_log": map[string]interface{}{
					"tokenName":       12345,          // wrong type
					"tokenSymbol":     true,           // wrong type
					"tokenDecimals":   "18",           // wrong type
					"transactionHash": nil,            // nil value
					"blockNumber":     "not_a_number", // wrong type
				},
			},
			expectedResult: true,
			description:    "Should handle wrong types in transfer_log gracefully",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// This should not panic regardless of input
			result := buildEventTriggerOutput(test.input)

			// Verify we always get a non-nil result
			require.NotNil(t, result, "buildEventTriggerOutput should never return nil")

			// Verify the result is a valid EventTrigger_Output structure
			require.IsType(t, &avsproto.EventTrigger_Output{}, result, "Should return correct type")

			t.Logf("✅ %s: %s", test.name, test.description)

			// For valid data cases, verify the structure is populated correctly
			if test.name == "Found=true with valid transfer_log structure" {
				require.NotNil(t, result.GetTransferLog(), "TransferLog should be populated")
				transferLog := result.GetTransferLog()
				require.Equal(t, "TestToken", transferLog.TokenName)
				require.Equal(t, "TEST", transferLog.TokenSymbol)
				require.Equal(t, uint32(18), transferLog.TokenDecimals)
				require.Equal(t, "0x123", transferLog.TransactionHash)
			} else if test.name == "Found=true with valid evm_log structure" {
				require.NotNil(t, result.GetEvmLog(), "EvmLog should be populated")
				evmLog := result.GetEvmLog()
				require.Equal(t, "0x123", evmLog.Address)
				require.Equal(t, []string{"0xtopic1", "0xtopic2"}, evmLog.Topics)
				require.Equal(t, "0xdata", evmLog.Data)
				require.Equal(t, uint64(12345), evmLog.BlockNumber)
			} else if test.name == "Found=true with transfer_log containing wrong types" {
				// When found=true with transfer_log key present (even with wrong types),
				// an empty TransferLog structure should be created
				require.NotNil(t, result.GetTransferLog(), "TransferLog should be created for malformed transfer_log data")
				transferLog := result.GetTransferLog()
				// All fields should be empty/default since type assertions failed
				require.Equal(t, "", transferLog.TokenName, "TokenName should be empty for wrong type")
				require.Equal(t, "", transferLog.TokenSymbol, "TokenSymbol should be empty for wrong type")
				require.Equal(t, uint32(0), transferLog.TokenDecimals, "TokenDecimals should be 0 for wrong type")
			} else {
				// For all other cases (nil, empty, invalid, etc.), should have empty oneof
				require.Nil(t, result.GetTransferLog(), "TransferLog should be nil for invalid/empty input")
				require.Nil(t, result.GetEvmLog(), "EvmLog should be nil for invalid/empty input")
			}
		})
	}
}

// TestBuildEventTriggerOutputConsistencyWithOtherTriggerFunctions tests that
// buildEventTriggerOutput follows the same defensive patterns as other trigger build functions.
func TestBuildEventTriggerOutputConsistencyWithOtherTriggerFunctions(t *testing.T) {
	t.Run("Nil handling consistency", func(t *testing.T) {
		// All trigger build functions should handle nil gracefully
		eventResult := buildEventTriggerOutput(nil)
		blockResult := buildBlockTriggerOutput(nil)
		fixedTimeResult := buildFixedTimeTriggerOutput(nil)
		cronResult := buildCronTriggerOutput(nil)
		manualResult := buildManualTriggerOutput(nil)

		// All should return non-nil results
		require.NotNil(t, eventResult, "buildEventTriggerOutput should handle nil gracefully")
		require.NotNil(t, blockResult, "buildBlockTriggerOutput should handle nil gracefully")
		require.NotNil(t, fixedTimeResult, "buildFixedTimeTriggerOutput should handle nil gracefully")
		require.NotNil(t, cronResult, "buildCronTriggerOutput should handle nil gracefully")
		require.NotNil(t, manualResult, "buildManualTriggerOutput should handle nil gracefully")

		t.Log("✅ All trigger build functions consistently handle nil inputs")
	})

	t.Run("Empty map handling consistency", func(t *testing.T) {
		emptyMap := map[string]interface{}{}

		// All trigger build functions should handle empty maps gracefully
		eventResult := buildEventTriggerOutput(emptyMap)
		blockResult := buildBlockTriggerOutput(emptyMap)
		fixedTimeResult := buildFixedTimeTriggerOutput(emptyMap)
		cronResult := buildCronTriggerOutput(emptyMap)
		manualResult := buildManualTriggerOutput(emptyMap)

		// All should return non-nil results with default values
		require.NotNil(t, eventResult, "buildEventTriggerOutput should handle empty map gracefully")
		require.NotNil(t, blockResult, "buildBlockTriggerOutput should handle empty map gracefully")
		require.NotNil(t, fixedTimeResult, "buildFixedTimeTriggerOutput should handle empty map gracefully")
		require.NotNil(t, cronResult, "buildCronTriggerOutput should handle empty map gracefully")
		require.NotNil(t, manualResult, "buildManualTriggerOutput should handle empty map gracefully")

		// Verify default values are set appropriately
		require.Equal(t, uint64(0), blockResult.BlockNumber, "BlockTrigger should have default block number")
		require.Equal(t, uint64(0), fixedTimeResult.Timestamp, "FixedTimeTrigger should have default timestamp")
		require.Equal(t, uint64(0), cronResult.Timestamp, "CronTrigger should have default timestamp")
		require.Greater(t, manualResult.RunAt, uint64(0), "ManualTrigger should have current timestamp as default")

		t.Log("✅ All trigger build functions consistently handle empty maps with appropriate defaults")
	})
}

// TestBuildTriggerDataMapFromProtobufEventTriggerComprehensive tests that
// buildTriggerDataMapFromProtobuf for EVENT trigger types consolidates all required fields
// compared to any previous explicit mapping and handles all edge cases properly.
func TestBuildTriggerDataMapFromProtobufEventTriggerComprehensive(t *testing.T) {
	tests := []struct {
		name        string
		input       interface{}
		description string
		verifyFunc  func(t *testing.T, result map[string]interface{})
	}{
		{
			name:        "Nil EventTrigger_Output",
			input:       nil,
			description: "Should handle nil input gracefully and add trigger type",
			verifyFunc: func(t *testing.T, result map[string]interface{}) {
				require.Equal(t, "TRIGGER_TYPE_EVENT", result["type"], "Should add trigger type for nil input")
				require.Len(t, result, 1, "Should only contain type field for nil input")
			},
		},
		{
			name: "EventTrigger_Output with complete TransferLog",
			input: &avsproto.EventTrigger_Output{
				OutputType: &avsproto.EventTrigger_Output_TransferLog{
					TransferLog: &avsproto.EventTrigger_TransferLogOutput{
						TokenName:        "Test Token",
						TokenSymbol:      "TEST",
						TokenDecimals:    18,
						TransactionHash:  "0x1234567890abcdef",
						Address:          "0xabcdef1234567890",
						BlockNumber:      12345678,
						BlockTimestamp:   1672531200,
						FromAddress:      "0x1111111111111111",
						ToAddress:        "0x2222222222222222",
						Value:            "1000000000000000000",
						ValueFormatted:   "1.0",
						TransactionIndex: 5,
						LogIndex:         3,
					},
				},
			},
			description: "Should map all TransferLog fields including the critical log_index field",
			verifyFunc: func(t *testing.T, result map[string]interface{}) {
				expected := map[string]interface{}{
					"token_name":        "Test Token",
					"token_symbol":      "TEST",
					"token_decimals":    uint32(18),
					"transaction_hash":  "0x1234567890abcdef",
					"address":           "0xabcdef1234567890",
					"block_number":      uint64(12345678),
					"block_timestamp":   uint64(1672531200),
					"from_address":      "0x1111111111111111",
					"to_address":        "0x2222222222222222",
					"value":             "1000000000000000000",
					"value_formatted":   "1.0",
					"transaction_index": uint32(5),
					"log_index":         uint32(3), // CRITICAL: This was missing before the fix
					"type":              "TRIGGER_TYPE_EVENT",
				}

				require.Equal(t, expected, result, "All TransferLog fields should be properly mapped")

				// Verify critical fields specifically
				require.Contains(t, result, "log_index", "log_index field should be present")
				require.Equal(t, uint32(3), result["log_index"], "log_index should have correct value")
			},
		},
		{
			name: "EventTrigger_Output with complete EvmLog",
			input: &avsproto.EventTrigger_Output{
				OutputType: &avsproto.EventTrigger_Output_EvmLog{
					EvmLog: &avsproto.Evm_Log{
						Address:          "0xabcdef1234567890",
						Topics:           []string{"0xtopic1", "0xtopic2", "0xtopic3"},
						Data:             "0xdeadbeef",
						BlockNumber:      12345678,
						TransactionHash:  "0x1234567890abcdef",
						TransactionIndex: 5,
						BlockHash:        "0xblockhash123456",
						Index:            3,
						Removed:          false,
					},
				},
			},
			description: "Should map all EvmLog fields including log_index",
			verifyFunc: func(t *testing.T, result map[string]interface{}) {
				expected := map[string]interface{}{
					"block_number":      uint64(12345678),
					"log_index":         uint32(3),
					"tx_hash":           "0x1234567890abcdef",
					"address":           "0xabcdef1234567890",
					"topics":            []string{"0xtopic1", "0xtopic2", "0xtopic3"},
					"data":              "0xdeadbeef",
					"block_hash":        "0xblockhash123456",
					"transaction_index": uint32(5),
					"removed":           false,
					"type":              "TRIGGER_TYPE_EVENT",
				}

				require.Equal(t, expected, result, "All EvmLog fields should be properly mapped")
			},
		},
		{
			name: "EventTrigger_Output with empty TransferLog",
			input: &avsproto.EventTrigger_Output{
				OutputType: &avsproto.EventTrigger_Output_TransferLog{
					TransferLog: &avsproto.EventTrigger_TransferLogOutput{},
				},
			},
			description: "Should handle empty TransferLog with default values",
			verifyFunc: func(t *testing.T, result map[string]interface{}) {
				expected := map[string]interface{}{
					"token_name":        "",
					"token_symbol":      "",
					"token_decimals":    uint32(0),
					"transaction_hash":  "",
					"address":           "",
					"block_number":      uint64(0),
					"block_timestamp":   uint64(0),
					"from_address":      "",
					"to_address":        "",
					"value":             "",
					"value_formatted":   "",
					"transaction_index": uint32(0),
					"log_index":         uint32(0),
					"type":              "TRIGGER_TYPE_EVENT",
				}

				require.Equal(t, expected, result, "Empty TransferLog should have default values")
			},
		},
		{
			name: "EventTrigger_Output with empty EvmLog",
			input: &avsproto.EventTrigger_Output{
				OutputType: &avsproto.EventTrigger_Output_EvmLog{
					EvmLog: &avsproto.Evm_Log{},
				},
			},
			description: "Should handle empty EvmLog with default values",
			verifyFunc: func(t *testing.T, result map[string]interface{}) {
				expected := map[string]interface{}{
					"block_number":      uint64(0),
					"log_index":         uint32(0),
					"tx_hash":           "",
					"address":           "",
					"topics":            ([]string)(nil),
					"data":              "",
					"block_hash":        "",
					"transaction_index": uint32(0),
					"removed":           false,
					"type":              "TRIGGER_TYPE_EVENT",
				}

				require.Equal(t, expected, result, "Empty EvmLog should have default values")
			},
		},
		{
			name:  "EventTrigger_Output with no output type set",
			input: &avsproto.EventTrigger_Output{
				// OutputType is nil (neither TransferLog nor EvmLog)
			},
			description: "Should handle EventTrigger_Output with no output type gracefully",
			verifyFunc: func(t *testing.T, result map[string]interface{}) {
				expected := map[string]interface{}{
					"type": "TRIGGER_TYPE_EVENT",
				}

				require.Equal(t, expected, result, "Should only contain type field when no output type is set")
			},
		},
		{
			name:        "Wrong type passed (not EventTrigger_Output)",
			input:       &avsproto.BlockTrigger_Output{},
			description: "Should handle wrong type gracefully and only add type field",
			verifyFunc: func(t *testing.T, result map[string]interface{}) {
				expected := map[string]interface{}{
					"type": "TRIGGER_TYPE_EVENT",
				}

				require.Equal(t, expected, result, "Should only contain type field for wrong input type")
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Execute the function under test
			result := buildTriggerDataMapFromProtobuf(avsproto.TriggerType_TRIGGER_TYPE_EVENT, test.input)

			// Verify results using the test-specific verification function
			test.verifyFunc(t, result)

			t.Logf("✅ %s: %s", test.name, test.description)
		})
	}
}

// TestBuildTriggerDataMapFromProtobufFieldCompleteness verifies that all fields
// from the protobuf structures are properly mapped to JavaScript-accessible names.
func TestBuildTriggerDataMapFromProtobufFieldCompleteness(t *testing.T) {
	t.Run("TransferLog field completeness", func(t *testing.T) {
		// Create a TransferLog with all possible fields set
		transferLog := &avsproto.EventTrigger_TransferLogOutput{
			TokenName:        "Complete Token",
			TokenSymbol:      "COMP",
			TokenDecimals:    18,
			TransactionHash:  "0xhash",
			Address:          "0xaddr",
			BlockNumber:      123,
			BlockTimestamp:   456,
			FromAddress:      "0xfrom",
			ToAddress:        "0xto",
			Value:            "789",
			ValueFormatted:   "0.789",
			TransactionIndex: 1,
			LogIndex:         2,
		}

		eventOutput := &avsproto.EventTrigger_Output{
			OutputType: &avsproto.EventTrigger_Output_TransferLog{
				TransferLog: transferLog,
			},
		}

		result := buildTriggerDataMapFromProtobuf(avsproto.TriggerType_TRIGGER_TYPE_EVENT, eventOutput)

		// Verify all TransferLog fields are mapped
		expectedFields := []string{
			"token_name", "token_symbol", "token_decimals",
			"transaction_hash", "address", "block_number", "block_timestamp",
			"from_address", "to_address", "value", "value_formatted",
			"transaction_index", "log_index", "type",
		}

		for _, field := range expectedFields {
			require.Contains(t, result, field, "Field %s should be present in result", field)
		}

		require.Len(t, result, len(expectedFields), "Result should contain exactly %d fields", len(expectedFields))

		t.Log("✅ All TransferLog protobuf fields are properly mapped to JavaScript names")
	})

	t.Run("EvmLog field completeness", func(t *testing.T) {
		// Create an EvmLog with all possible fields set
		evmLog := &avsproto.Evm_Log{
			Address:          "0xaddr",
			Topics:           []string{"0xtopic1", "0xtopic2"},
			Data:             "0xdata",
			BlockNumber:      123,
			TransactionHash:  "0xhash",
			TransactionIndex: 1,
			BlockHash:        "0xblockhash",
			Index:            2,
			Removed:          true,
		}

		eventOutput := &avsproto.EventTrigger_Output{
			OutputType: &avsproto.EventTrigger_Output_EvmLog{
				EvmLog: evmLog,
			},
		}

		result := buildTriggerDataMapFromProtobuf(avsproto.TriggerType_TRIGGER_TYPE_EVENT, eventOutput)

		// Verify all EvmLog fields are mapped
		expectedFields := []string{
			"block_number", "log_index", "tx_hash", "address",
			"topics", "data", "block_hash", "transaction_index",
			"removed", "type",
		}

		for _, field := range expectedFields {
			require.Contains(t, result, field, "Field %s should be present in result", field)
		}

		require.Len(t, result, len(expectedFields), "Result should contain exactly %d fields", len(expectedFields))

		t.Log("✅ All EvmLog protobuf fields are properly mapped to JavaScript names")
	})
}
