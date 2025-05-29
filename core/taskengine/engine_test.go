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

	resultTrigger, err := n.TriggerTask(testutil.TestUser1(), &avsproto.UserTriggerTaskReq{
		TaskId: result.Id,
		Reason: &avsproto.TriggerReason{
			BlockNumber: uint64(101),
			Type:        avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
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

	if execution.TriggerName != tr1.Trigger.Name {
		t.Errorf("invalid triggered name. expect %s got %s", tr1.Trigger.Name, execution.TriggerName)
	}

	if execution.Id != resultTrigger.ExecutionId {
		t.Errorf("invalid execution id. expect %s got %s", resultTrigger.ExecutionId, execution.Id)
	}

	if execution.Reason == nil || execution.Reason.BlockNumber != 101 {
		var actualBlockNumber uint64
		if execution.Reason != nil {
			actualBlockNumber = execution.Reason.BlockNumber
		}
		t.Errorf("invalid triggered block. expect 101 got %d (Reason: %+v)", actualBlockNumber, execution.Reason)
	}

	// Another user cannot get this execution id
	execution, err = n.GetExecution(testutil.TestUser2(), &avsproto.ExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})
	if err == nil || execution != nil {
		t.Errorf("expected failure getting other user execution but successfully read it")
	}
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
	if len(resp.Items) <= 2 {
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
		t.Errorf("expect only default wallet but got %d", len(resp.Items))
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

	resultTrigger, err := n.TriggerTask(testutil.TestUser1(), &avsproto.UserTriggerTaskReq{
		TaskId: result.Id,
		Reason: &avsproto.TriggerReason{
			BlockNumber: uint64(101),
			Type:        avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		},
		IsBlocking: true,
	})

	if err != nil {
		t.Errorf("expected trigger successfully but got error: %s", err)
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

	if execution.Id != resultTrigger.ExecutionId {
		t.Errorf("invalid execution id. expect %s got %s", resultTrigger.ExecutionId, execution.Id)
	}

	if execution.TriggerName != tr1.Trigger.Name {
		t.Errorf("invalid triggered name. expect %s got %s", tr1.Trigger.Name, execution.TriggerName)
	}

	if execution.Reason == nil || execution.Reason.BlockNumber != 101 {
		var actualBlockNumber uint64
		if execution.Reason != nil {
			actualBlockNumber = execution.Reason.BlockNumber
		}
		t.Errorf("invalid triggered block. expect 101 got %d (Reason: %+v)", actualBlockNumber, execution.Reason)
	}
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

	resultTrigger, err := n.TriggerTask(testutil.TestUser1(), &avsproto.UserTriggerTaskReq{
		TaskId: result.Id,
		Reason: &avsproto.TriggerReason{
			BlockNumber: uint64(101),
			Type:        avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		},
		IsBlocking: false,
	})

	if err != nil {
		t.Errorf("expected trigger successfully but got error: %s", err)
	}

	// Now get back that execution id, because the task is run async we won't have any data yet,
	// just the status for now
	executionStatus, err := n.GetExecutionStatus(testutil.TestUser1(), &avsproto.ExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})

	if executionStatus.Status != avsproto.ExecutionStatus_Queued {
		t.Errorf("invalid execution status, expected queue but got %s", avsproto.TaskStatus_name[int32(executionStatus.Status)])
	}

	// Now let the queue start and process job
	// In our end to end system the worker will process the job eventually
	worker.ProcessSignal(1)

	execution, err := n.GetExecution(testutil.TestUser1(), &avsproto.ExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})

	if execution.TriggerName != tr1.Trigger.Name {
		t.Errorf("invalid triggered name. expect %s got %s", tr1.Trigger.Name, execution.TriggerName)
	}
	if execution.Id != resultTrigger.ExecutionId {
		t.Errorf("wring execution id, expected %s got %s", resultTrigger.ExecutionId, execution.Id)
	}

	if !execution.Success {
		t.Errorf("wrong success result, expected true got false")
	}

	if execution.Steps[0].NodeId != "ping1" {
		t.Errorf("wrong node id in execution log")
	}

	if len(execution.Steps) == 0 {
		t.Errorf("No execution steps found")
		return
	}

	step := execution.Steps[0]
	if step.GetRestApi() == nil {
		t.Errorf("RestApi data is nil")
		return
	}

	// Get the response data as a map
	responseData := gow.AnyToMap(step.GetRestApi().Data)
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

	if executionStatus.Status != avsproto.ExecutionStatus_Finished {
		t.Errorf("invalid execution status, expected completed but got %s", avsproto.TaskStatus_name[int32(executionStatus.Status)])
	}

	// Verify TaskTriggerKey is cleaned up after successful async execution
	triggerKeyBytes := TaskTriggerKey(result, resultTrigger.ExecutionId)
	val, errDbRead := db.GetKey(triggerKeyBytes)
	if errDbRead == nil {
		t.Errorf("Expected TaskTriggerKey '%s' to be deleted after async execution, but it was found with value: %s", string(triggerKeyBytes), string(val))
	} else if !strings.Contains(errDbRead.Error(), "Key not found") {
		// Allow "Key not found", but log other errors
		t.Logf("Got an unexpected error when checking for deleted TaskTriggerKey '%s': %v. This might be okay if it implies not found.", string(triggerKeyBytes), errDbRead)
	}
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

	resultTrigger, err := n.TriggerTask(testutil.TestUser1(), &avsproto.UserTriggerTaskReq{
		TaskId: result.Id,
		Reason: &avsproto.TriggerReason{
			BlockNumber: uint64(101),
			Type:        avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		},
		IsBlocking: true,
	})

	if err != nil || resultTrigger == nil {
		t.Errorf("expected trigger successfully but got error: %s", err)
	}

	// Now the task has reach its max run, and canot run anymore
	resultTrigger, err = n.TriggerTask(testutil.TestUser1(), &avsproto.UserTriggerTaskReq{
		TaskId: result.Id,
		Reason: &avsproto.TriggerReason{
			BlockNumber: uint64(101),
			Type:        avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		},
		IsBlocking: true,
	})

	if err == nil || resultTrigger != nil {
		t.Errorf("expect trigger error but succeed")
	}
}
