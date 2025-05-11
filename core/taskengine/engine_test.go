package taskengine

import (
	"fmt"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/apqueue"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
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
		t.Errorf("expect list task succesfully but got error %s", err)
	}

	if result == nil {
		t.Errorf("expect result is not nil but got nil")
	}

	if len(result.Items) != 1 {
		t.Errorf("list task return wrong. expect 1, got %d", len(result.Items))
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

	for i := range 20 {
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
		ItemPerPage: 5,
	})

	if err != nil {
		t.Errorf("expect list task succesfully but got error %s", err)
	}

	if !result.HasMore {
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

	if result.Cursor == "" {
		t.Errorf("list task returns wrong cursor. expect non empty, got none")
	}
	result, err = n.ListTasksByUser(testutil.TestUser1(), &avsproto.ListTasksReq{
		SmartWalletAddress: []string{
			"0x961d2DD008960A9777571D78D21Ec9C3E5c6020c",
			"0x5D36dCdB35D0C85D88C5AA31E37cac165B480ba4",
		},
		ItemPerPage: 15,
		Cursor:      result.Cursor,
	})

	if len(result.Items) != 15 {
		t.Errorf("list task returns wrong. expect 15, got %d", len(result.Items))
	}
	if result.Items[0].Name != "t3_15" || result.Items[2].Name != "t3_13" || result.Items[14].Name != "t3_1" {
		t.Errorf("list task returns wrong task result, expected t3_15 t3_13 t3_1 got %s %s %s", result.Items[0].Name, result.Items[2].Name, result.Items[14].Name)
	}

	if !result.HasMore {
		t.Errorf("expect hasmore is true, but got false")
	}

	result, err = n.ListTasksByUser(testutil.TestUser1(), &avsproto.ListTasksReq{
		SmartWalletAddress: []string{
			"0x961d2DD008960A9777571D78D21Ec9C3E5c6020c",
			"0x5D36dCdB35D0C85D88C5AA31E37cac165B480ba4",
		},
		ItemPerPage: 15,
		Cursor:      result.Cursor,
	})

	if len(result.Items) != 2 {
		t.Errorf("list task returns wrong. expect 2, got %d", len(result.Items))
	}
	if result.Items[0].Name != "t3_0" || result.Items[1].Name != "t2_1" {
		t.Errorf("list task returns wrong task result, expected t3_15 t3_1 got %s %s", result.Items[0].Name, result.Items[1].Name)
	}
	if result.HasMore {
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
			BlockNumber: 101,
		},
		IsBlocking: true,
	})

	// Now get back that execution data using the log
	execution, err := n.GetExecution(testutil.TestUser1(), &avsproto.ExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})

	if execution.TriggerName != tr1.Trigger.Name {
		t.Errorf("invalid triggered name. expect %s got %s", tr1.Trigger.Name, execution.TriggerName)
	}

	if execution.Id != resultTrigger.ExecutionId {
		t.Errorf("invalid execution id. expect %s got %s", resultTrigger.ExecutionId, execution.Id)
	}

	if execution.Reason.BlockNumber != 101 {
		t.Errorf("invalid triggered block. expect 101 got %d", execution.Reason.BlockNumber)
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

	wallets, _ := n.GetSmartWallets(u.Address, nil)
	if len(wallets) <= 2 {
		t.Errorf("expect 3 smartwallets but got %d", len(wallets))
	}

	// The default wallet with salt 0
	if wallets[0].Address != "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6" {
		t.Errorf("invalid smartwallet address, expect 0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6 got %s", wallets[0].Address)
	}

	// This is the wallet from custom factory https://sepolia.etherscan.io/address/0x9406Cc6185a346906296840746125a0E44976454#readProxyContract
	if wallets[1].Address != "0x29C3139e460d03d951070596eED3218B3cc34FD1" {
		t.Errorf("invalid smartwallet address, expect 0x923A6A90E422871FC56020d560Bc0D0CF1fbb93e got %s", wallets[1].Address)
	}

	// the wallet with default factory and salt 12345
	if wallets[2].Address != "0x961d2DD008960A9777571D78D21Ec9C3E5c6020c" {
		t.Errorf("invalid smartwallet address, expect 0x961d2DD008960A9777571D78D21Ec9C3E5c6020c got %s", wallets[2].Address)
	}

	wallets, _ = n.GetSmartWallets(u.Address, &avsproto.ListWalletReq{
		FactoryAddress: "0x9406Cc6185a346906296840746125a0E44976454",
	})
	if len(wallets) != 1 {
		t.Errorf("expect 1 smartwallet but got %d", len(wallets))
	}
	// owner 0xD7050816337a3f8f690F8083B5Ff8019D50c0E50 salt 0 https://sepolia.etherscan.io/address/0x29adA1b5217242DEaBB142BC3b1bCfFdd56008e7#readContract
	if wallets[0].Address != "0x29C3139e460d03d951070596eED3218B3cc34FD1" {
		t.Errorf("invalid smartwallet address, expect 0x29C3139e460d03d951070596eED3218B3cc34FD1 got %s", wallets[0].Address)
	}

	if wallets[0].Salt != "9876" {
		t.Errorf("invalid smartwallet address salt, expect 9876 got %s", wallets[0].Salt)
	}

	// other user will not be able to list above wallet
	wallets, _ = n.GetSmartWallets(testutil.TestUser2().Address, nil)
	if len(wallets) != 1 {
		t.Errorf("expect only default wallet but got %d", len(wallets))
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
			BlockNumber: 101,
		},
		IsBlocking: true,
	})

	if err != nil {
		t.Errorf("expected trigger succesfully but got error: %s", err)
	}

	// Now get back that execution id
	execution, err := n.GetExecution(testutil.TestUser1(), &avsproto.ExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})

	if execution.Id != resultTrigger.ExecutionId {
		t.Errorf("invalid execution id. expect %s got %s", resultTrigger.ExecutionId, execution.Id)
	}

	if execution.TriggerName != tr1.Trigger.Name {
		t.Errorf("invalid triggered name. expect %s got %s", tr1.Trigger.Name, execution.TriggerName)
	}
	if execution.Reason.BlockNumber != 101 {
		t.Errorf("invalid triggered block. expect 101 got %d", execution.Reason.BlockNumber)
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
			BlockNumber: 101,
		},
		IsBlocking: false,
	})

	if err != nil {
		t.Errorf("expected trigger succesfully but got error: %s", err)
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

	if !strings.Contains(gow.AnyToString(execution.Steps[0].GetRestApi().Data), "httpbin.org") {
		t.Error("Invalid output data")
	}

	// If we get the status back it also reflected
	executionStatus, err = n.GetExecutionStatus(testutil.TestUser1(), &avsproto.ExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})

	if executionStatus.Status != avsproto.ExecutionStatus_Finished {
		t.Errorf("invalid execution status, expected completed but got %s", avsproto.TaskStatus_name[int32(executionStatus.Status)])
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
			BlockNumber: 101,
		},
		IsBlocking: true,
	})

	if err != nil || resultTrigger == nil {
		t.Errorf("expected trigger succesfully but got error: %s", err)
	}

	// Now the task has reach its max run, and canot run anymore
	resultTrigger, err = n.TriggerTask(testutil.TestUser1(), &avsproto.UserTriggerTaskReq{
		TaskId: result.Id,
		Reason: &avsproto.TriggerReason{
			BlockNumber: 101,
		},
		IsBlocking: true,
	})

	if err == nil || resultTrigger != nil {
		t.Errorf("expect trigger error but succeed")
	}
}

func TestCreateSecret(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	user := testutil.TestUser1()
	n.CreateSecret(user, &avsproto.CreateOrUpdateSecretReq{
		Name:   "telebot",
		Secret: "123",
	})

	result, _ := n.ListSecrets(user, &avsproto.ListSecretsReq{})
	if len(result.Items) != 1 {
		t.Errorf("invalid secret result, expect 1 item got %d", len(result.Items))
	}

	if result.Items[0].Name != "telebot" {
		t.Errorf("invalid secret name, expect telebot got %s", result.Items[0].Name)
	}
}

func TestCreateSecretAtCorrectLevel(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	user1 := testutil.TestUser1()
	user2 := testutil.TestUser2()
	n.CreateSecret(user1, &avsproto.CreateOrUpdateSecretReq{
		Name:   "svckey",
		Secret: "secret123",
	})

	n.CreateSecret(user2, &avsproto.CreateOrUpdateSecretReq{
		Name:       "svckey",
		Secret:     "secret456",
		WorkflowId: "workflow123",
	})

	//"secret:_:%s:_:%s",
	key1, _ := n.db.GetKey([]byte(fmt.Sprintf("secret:_:%s:_:%s", strings.ToLower(user1.Address.Hex()), "svckey")))
	if string(key1) != "secret123" {
		t.Errorf("expect secret to be create at user level with value secret123 but got %s", string(key1))
	}
	key2, _ := n.db.GetKey([]byte(fmt.Sprintf("secret:_:%s:%s:%s", strings.ToLower(user2.Address.Hex()), "workflow123", "svckey")))
	if string(key2) != "secret456" {
		t.Errorf("expect secret to be create at user level with value secret456 but got %s", string(key1))
	}
}

func TestCreateSecretListMulti(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	user := testutil.TestUser1()
	n.CreateSecret(user, &avsproto.CreateOrUpdateSecretReq{
		Name:   "telebot",
		Secret: "123",
	})

	n.CreateSecret(user, &avsproto.CreateOrUpdateSecretReq{
		Name:   "telebot2",
		Secret: "456",
	})

	result, _ := n.ListSecrets(user, &avsproto.ListSecretsReq{})
	if len(result.Items) != 2 {
		t.Errorf("invalid secret result, expect 2 items got %d", len(result.Items))
	}

	if result.Items[0].Name != "telebot" || result.Items[1].Name != "telebot2" {
		t.Errorf("invalid secret name, expect [telebot, telebot2] got %s", result)
	}
}

func TestUpdateSecretSucceed(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	user := testutil.TestUser1()
	n.CreateSecret(user, &avsproto.CreateOrUpdateSecretReq{
		Name:   "telebot",
		Secret: "123",
	})

	key, err := SecretStorageKey(&model.Secret{
		User: user,
		Name: "telebot",
	})

	value, err := db.GetKey([]byte(key))

	if err != nil {
		t.Errorf("expect secret existed but found error %s", err)
	}

	n.UpdateSecret(user, &avsproto.CreateOrUpdateSecretReq{
		Name:   "telebot",
		Secret: "4567",
	})

	value, err = db.GetKey([]byte(key))

	if err != nil {
		t.Errorf("expect secret existed but found error %s", err)
	}
	if string(value) != "4567" {
		t.Errorf("expect secrect value is 4567 but got %s", string(value))
	}
}

func TestCannotUpdateSecretOfOther(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	user1 := testutil.TestUser1()
	user2 := testutil.TestUser2()
	n.CreateSecret(user1, &avsproto.CreateOrUpdateSecretReq{
		Name:   "telebot",
		Secret: "123",
	})

	n.UpdateSecret(user2, &avsproto.CreateOrUpdateSecretReq{
		Name:   "telebot",
		Secret: "4567",
	})

	key, err := SecretStorageKey(&model.Secret{
		User: user1,
		Name: "telebot",
	})

	value, err := db.GetKey([]byte(key))

	if err != nil {
		t.Errorf("expect secret existed but found error %s", err)
	}
	if string(value) != "123" {
		t.Errorf("expect secrect value is 4567 but got %s", string(value))
	}
}

func TestCannotUpdateNotExistSecret(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	user1 := testutil.TestUser1()
	result, err := n.UpdateSecret(user1, &avsproto.CreateOrUpdateSecretReq{
		Name:   "telebot",
		Secret: "4567",
	})

	if result || err == nil || err.Error() != "rpc error: code = NotFound desc = Secret not found" {
		t.Errorf("expect a failure when updating secret but no error was raise: %s", err)
	}
}

func TestDeleteSecretSucceed(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	user := testutil.TestUser1()
	n.CreateSecret(user, &avsproto.CreateOrUpdateSecretReq{
		Name:   "telebot",
		Secret: "123",
	})

	// Ensure the secet is created
	result, _ := n.ListSecrets(user, &avsproto.ListSecretsReq{})
	if result.Items[0].Name != "telebot" {
		t.Errorf("invalid secret name, expect telebot got %s", result)
	}

	// Now the user can remove it
	n.DeleteSecret(user, &avsproto.DeleteSecretReq{
		Name: "telebot",
	})

	result, _ = n.ListSecrets(user, &avsproto.ListSecretsReq{})
	if len(result.Items) != 0 {
		t.Errorf("secret should be delete but still accessible")
	}
}

func TestDeleteSecretAtCorrectUserLevel(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	user1 := testutil.TestUser1()
	n.CreateSecret(user1, &avsproto.CreateOrUpdateSecretReq{
		Name:   "svckey",
		Secret: "secret123",
	})

	n.CreateSecret(user1, &avsproto.CreateOrUpdateSecretReq{
		Name:       "svckey",
		Secret:     "secret456",
		WorkflowId: "workflow123",
	})

	n.DeleteSecret(user1, &avsproto.DeleteSecretReq{
		Name: "svckey",
	})

	key1 := fmt.Sprintf("secret:_:%s:_:%s", strings.ToLower(user1.Address.Hex()), "svckey")
	key2 := fmt.Sprintf("secret:_:%s:%s:%s", strings.ToLower(user1.Address.Hex()), "workflow123", "svckey")

	if ok, _ := n.db.Exist([]byte(key1)); ok {
		t.Errorf("expect secret to be deleted at user level but it still exists")
	}

	if ok, _ := n.db.Exist([]byte(key2)); !ok {
		t.Errorf("expect secret to be deleted at workflow level but it doesn't")
	}
}

func TestDeleteSecretAtCorrectWorkflowLevel(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	user1 := testutil.TestUser1()
	n.CreateSecret(user1, &avsproto.CreateOrUpdateSecretReq{
		Name:   "svckey",
		Secret: "secret123",
	})

	n.CreateSecret(user1, &avsproto.CreateOrUpdateSecretReq{
		Name:       "svckey",
		Secret:     "secret456",
		WorkflowId: "workflow123",
	})

	n.DeleteSecret(user1, &avsproto.DeleteSecretReq{
		Name:       "svckey",
		WorkflowId: "workflow123",
	})

	key1 := fmt.Sprintf("secret:_:%s:_:%s", strings.ToLower(user1.Address.Hex()), "svckey")
	key2 := fmt.Sprintf("secret:_:%s:%s:%s", strings.ToLower(user1.Address.Hex()), "workflow123", "svckey")

	if ok, _ := n.db.Exist([]byte(key1)); !ok {
		t.Errorf("expect secret to be deleted at user level but it still exists")
	}

	if ok, _ := n.db.Exist([]byte(key2)); ok {
		t.Errorf("expect secret to be deleted at workflow level but it doesn't")
	}
}

func TestListSecrets(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	user1 := testutil.TestUser1()
	user2 := testutil.TestUser2()
	n.CreateSecret(user1, &avsproto.CreateOrUpdateSecretReq{
		Name:   "telebot",
		Secret: "secret123",
	})

	n.CreateSecret(user1, &avsproto.CreateOrUpdateSecretReq{
		Name:       "telebot2",
		Secret:     "secretworkflow123",
		WorkflowId: "workflow123",
	})

	n.CreateSecret(user2, &avsproto.CreateOrUpdateSecretReq{
		Name:       "token123",
		Secret:     "secretworkflow456",
		WorkflowId: "worflow123",
	})

	result, _ := n.ListSecrets(user1, &avsproto.ListSecretsReq{})
	if len(result.Items) != 2 {
		t.Errorf("invalid secret result, expect 2 items got %d", len(result.Items))
	}
	if result.Items[0].Name != "telebot" || result.Items[1].Name != "telebot2" {
		t.Errorf("invalid secret name, expect [telebot, telebot2] got %s", result)
	}

	result2, _ := n.ListSecrets(user2, &avsproto.ListSecretsReq{})
	if len(result2.Items) != 1 {
		t.Errorf("invalid secret result, expect 2 items got %d", len(result.Items))
	}
	if result2.Items[0].Name != "token123" {
		t.Errorf("invalid secret name, expect [token123] got %s", result)
	}

}

func TestGetWalletReturnTaskStat(t *testing.T) {
	db := testutil.TestMustDB()
	//defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	user1 := testutil.TestUser1()
	// Now create a test task
	tr1 := testutil.RestTask()
	tr1.Name = "t1"
	// salt 0 wallet
	tr1.SmartWalletAddress = "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"

	result, _ := n.GetWallet(user1, &avsproto.GetWalletReq{
		Salt: "0",
	})

	if result.TotalTaskCount > 0 {
		t.Errorf("expect no task count yet but got :%d", result.TotalTaskCount)
	}

	taskResult, _ := n.CreateTask(user1, tr1)
	result, _ = n.GetWallet(user1, &avsproto.GetWalletReq{
		Salt: "0",
	})

	if result.TotalTaskCount != 1 || result.ActiveTaskCount != 1 || result.CompletedTaskCount != 0 {
		t.Errorf("expect total=1 active=1 completed=0 but got %v", result)
	}

	// Make the task run to simulate completed count
	n.TriggerTask(user1, &avsproto.UserTriggerTaskReq{
		TaskId: taskResult.Id,
		Reason: &avsproto.TriggerReason{
			BlockNumber: 101,
		},
		IsBlocking: true,
	})

	result, _ = n.GetWallet(user1, &avsproto.GetWalletReq{
		Salt: "0",
	})

	if result.TotalTaskCount != 1 || result.ActiveTaskCount != 1 || result.CompletedTaskCount != 0 {
		t.Errorf("expect total=1 active=0 completed=1 but got %v", result)
	}

	tr2 := testutil.JsFastTask()
	tr2.MaxExecution = 1
	task2, _ := n.CreateTask(user1, tr2)
	result, _ = n.GetWallet(user1, &avsproto.GetWalletReq{
		Salt: "0",
	})

	if result.TotalTaskCount != 2 || result.ActiveTaskCount != 2 || result.CompletedTaskCount != 0 {
		t.Errorf("expect total=2 active=0 completed=2 but got %v", result)
	}

	// Make the task run to simulate completed count
	n.TriggerTask(user1, &avsproto.UserTriggerTaskReq{
		TaskId: task2.Id,
		Reason: &avsproto.TriggerReason{
			BlockNumber: 101,
		},
		IsBlocking: true,
	})

	result, _ = n.GetWallet(user1, &avsproto.GetWalletReq{
		Salt: "0",
	})

	if result.TotalTaskCount != 2 || result.ActiveTaskCount != 1 || result.CompletedTaskCount != 1 {
		t.Errorf("expect total=2 active=1 completed=1 but got %v", result)
	}
}

func TestGetWorkflowCount(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	user1 := testutil.TestUser1()
	user2 := testutil.TestUser2()

	user1ExtraSmartWallet, _ := n.GetWallet(user1, &avsproto.GetWalletReq{
		Salt: "123",
	})

	// Create tasks for user1
	task1 := testutil.RestTask()
	task1.Name = "task1"
	// default wallet
	task1.SmartWalletAddress = "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"
	n.CreateTask(user1, task1)

	task2 := testutil.RestTask()
	task2.Name = "task2"
	task2.SmartWalletAddress = user1ExtraSmartWallet.Address
	n.CreateTask(user1, task2)

	// Create a task for user2
	task3 := testutil.RestTask()
	task3.Name = "task3"
	// default wallet for user2
	task3.SmartWalletAddress = "0xBdCcA49575918De45bb32f5ba75388e7c3fBB5e4"
	n.CreateTask(user2, task3)

	// Test task count for user1
	result, err := n.GetWorkflowCount(user1, &avsproto.GetWorkflowCountReq{})
	if err != nil {
		t.Errorf("expected to get task count successfully but got error: %s", err)
	}
	if result.Total != 2 {
		t.Errorf("expected task count 2 for user1 but got %d", result.Total)
	}

	// Test task count for user2
	result, err = n.GetWorkflowCount(user2, &avsproto.GetWorkflowCountReq{})
	if err != nil {
		t.Errorf("expected to get task count successfully but got error: %s", err)
	}
	if result.Total != 1 {
		t.Errorf("expected task count 1 for user2 but got %d", result.Total)
	}

	// Create extra tasks for a smart wallet
	task4 := testutil.RestTask()
	task4.Name = "walletTask1"
	task4.SmartWalletAddress = user1ExtraSmartWallet.Address
	n.CreateTask(user1, task4)

	task5 := testutil.RestTask()
	task5.Name = "walletTask2"
	task5.SmartWalletAddress = user1ExtraSmartWallet.Address
	n.CreateTask(user1, task5)

	// Test task count for the smart wallet
	result, err = n.GetWorkflowCount(user1, &avsproto.GetWorkflowCountReq{
		Addresses: []string{"0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"},
	})
	if result.Total != 1 {
		t.Errorf("expected task count 2 for smart wallet but got %d", result.Total)
	}

	// Test task count for the smart wallet
	result, err = n.GetWorkflowCount(user1, &avsproto.GetWorkflowCountReq{
		Addresses: []string{user1ExtraSmartWallet.Address},
	})
	if result.Total != 3 {
		t.Errorf("expected task count 3 for smart wallet but got %d", result.Total)
	}

	// Count other user smart wallet return 0
	result, err = n.GetWorkflowCount(user1, &avsproto.GetWorkflowCountReq{
		Addresses: []string{"0xBdCcA49575918De45bb32f5ba75388e7c3fBB5e4"},
	})
	if result.Total != 0 {
		t.Errorf("expected task count 0 for smart wallet but got %d", result.Total)
	}

	// Count other user smart wallet return 0
	result, err = n.GetWorkflowCount(user2, &avsproto.GetWorkflowCountReq{
		Addresses: []string{"0xBdCcA49575918De45bb32f5ba75388e7c3fBB5e4"},
	})
	if result.Total != 1 {
		t.Errorf("expected task count 1 for smart wallet but got %d", result.Total)
	}
}

func TestGetExecutionCount(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	// create 2 users to ensure task counting is done per user
	user1 := testutil.TestUser1()
	user2 := testutil.TestUser2()

	// create 2 tasks for user1, we will trigger them independently to test execution count per task
	tr1 := testutil.JsFastTask()
	tr1.Name = "t1"
	// salt 0 wallet
	tr1.SmartWalletAddress = "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"
	task1, _ := n.CreateTask(user1, tr1)

	tr2 := testutil.JsFastTask()
	tr2.Name = "t2"
	// salt 0 wallet
	tr2.SmartWalletAddress = "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"
	task2, _ := n.CreateTask(user1, tr2)

	tr3 := testutil.JsFastTask()
	tr3.Name = "t1"
	// salt 0 wallet
	tr3.SmartWalletAddress = "0xBdCcA49575918De45bb32f5ba75388e7c3fBB5e4"
	task3, _ := n.CreateTask(user2, tr3)

	// initial state everything is 0
	stat, _ := n.GetExecutionCount(user1, &avsproto.GetExecutionCountReq{})
	if stat.Total != 0 {
		t.Errorf("expected execution count 0 for user1 but got %d", stat.Total)
	}
	stat, _ = n.GetExecutionCount(user2, &avsproto.GetExecutionCountReq{
		WorkflowIds: []string{task1.Id},
	})
	if stat.Total != 0 {
		t.Errorf("expected execution count 0 for user2 but got %d", stat.Total)
	}

	stat, _ = n.GetExecutionCount(user2, &avsproto.GetExecutionCountReq{})
	if stat.Total != 0 {
		t.Errorf("expected execution count 0 for user2 but got %d", stat.Total)
	}

	stat, _ = n.GetExecutionCount(user2, &avsproto.GetExecutionCountReq{
		WorkflowIds: []string{task2.Id},
	})
	if stat.Total != 0 {
		t.Errorf("expected execution count 0 for user2 but got %d", stat.Total)
	}

	// trigger task1 and task2
	n.TriggerTask(user1, &avsproto.UserTriggerTaskReq{
		TaskId: task1.Id,
		Reason: &avsproto.TriggerReason{
			BlockNumber: 101,
		},
		IsBlocking: true,
	})
	n.TriggerTask(user1, &avsproto.UserTriggerTaskReq{
		TaskId: task2.Id,
		Reason: &avsproto.TriggerReason{
			BlockNumber: 101,
		},
		IsBlocking: true,
	})

	// now user1 has 1 execution for task1, user2 continue to have 0
	stat, _ = n.GetExecutionCount(user1, &avsproto.GetExecutionCountReq{})
	if stat.Total != 2 {
		t.Errorf("expected execution count 2 for user1 but got %d", stat.Total)
	}

	stat, _ = n.GetExecutionCount(user1, &avsproto.GetExecutionCountReq{
		WorkflowIds: []string{task1.Id},
	})
	if stat.Total != 1 {
		t.Errorf("expected execution count 1 for user1 but got %d", stat.Total)
	}
	stat, _ = n.GetExecutionCount(user1, &avsproto.GetExecutionCountReq{
		WorkflowIds: []string{task2.Id},
	})
	if stat.Total != 1 {
		t.Errorf("expected execution count 1 for user1 but got %d", stat.Total)
	}

	stat, _ = n.GetExecutionCount(user2, &avsproto.GetExecutionCountReq{})
	if stat.Total != 0 {
		t.Errorf("expected execution count 0 for user2 but got %d", stat.Total)
	}

	// Trigger task1 N more times, ensure only its count is increased
	for i := 0; i < 10; i++ {
		n.TriggerTask(user1, &avsproto.UserTriggerTaskReq{
			TaskId: task1.Id,
			Reason: &avsproto.TriggerReason{
				BlockNumber: 101,
			},
			IsBlocking: true,
		})
	}

	stat, _ = n.GetExecutionCount(user1, &avsproto.GetExecutionCountReq{})
	if stat.Total != 12 {
		t.Errorf("expected execution count 12 for user1 but got %d", stat.Total)
	}
	stat, _ = n.GetExecutionCount(user1, &avsproto.GetExecutionCountReq{
		WorkflowIds: []string{task1.Id},
	})
	if stat.Total != 11 {
		t.Errorf("expected execution count 11 for user1 but got %d", stat.Total)
	}

	// trigger user2 task 10 times, ensure only its count is increased and other user's task count is not affected
	for i := 0; i < 10; i++ {
		n.TriggerTask(user2, &avsproto.UserTriggerTaskReq{
			TaskId: task3.Id,
			Reason: &avsproto.TriggerReason{
				BlockNumber: 101,
			},
			IsBlocking: true,
		})
	}

	stat, _ = n.GetExecutionCount(user2, &avsproto.GetExecutionCountReq{})
	if stat.Total != 10 {
		t.Errorf("expected execution count 10 for user2 but got %d", stat.Total)
	}
	stat, _ = n.GetExecutionCount(user2, &avsproto.GetExecutionCountReq{
		WorkflowIds: []string{task3.Id},
	})
	if stat.Total != 10 {
		t.Errorf("expected execution count 10 for user2 but got %d", stat.Total)
	}

	stat, _ = n.GetExecutionCount(user1, &avsproto.GetExecutionCountReq{})
	if stat.Total != 12 {
		t.Errorf("expected execution count 12 for user1 but got %d", stat.Total)
	}
	stat, _ = n.GetExecutionCount(user1, &avsproto.GetExecutionCountReq{
		WorkflowIds: []string{task1.Id},
	})
	if stat.Total != 11 {
		t.Errorf("expected execution count 11 for user1/task1 but got %d", stat.Total)
	}

	stat, _ = n.GetExecutionCount(user1, &avsproto.GetExecutionCountReq{
		WorkflowIds: []string{task2.Id},
	})
	if stat.Total != 1 {
		t.Errorf("expected execution count 1 for user1/task2 but got %d", stat.Total)
	}
}
