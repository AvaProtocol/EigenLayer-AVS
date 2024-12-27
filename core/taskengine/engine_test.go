package taskengine

import (
	"fmt"
	"testing"

	"github.com/AvaProtocol/ap-avs/core/testutil"
	avsproto "github.com/AvaProtocol/ap-avs/protobuf"
	"github.com/AvaProtocol/ap-avs/storage"
)

func TestListTasks(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	n.CreateSmartWallet(testutil.TestUser1(), &avsproto.GetWalletReq{
		Salt: "12345",
	})
	n.CreateSmartWallet(testutil.TestUser1(), &avsproto.GetWalletReq{
		Salt: "6789",
	})

	// Now create a test task
	tr1 := testutil.RestTask()
	tr1.Memo = "t1"
	// salt 0
	tr1.SmartWalletAddress = "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"
	n.CreateTask(testutil.TestUser1(), tr1)

	tr2 := testutil.RestTask()
	tr2.Memo = "t2"
	// salt 12345
	tr2.SmartWalletAddress = "0x961d2DD008960A9777571D78D21Ec9C3E5c6020c"
	n.CreateTask(testutil.TestUser1(), tr2)

	tr3 := testutil.RestTask()
	// salt 6789
	tr3.Memo = "t3"
	tr3.SmartWalletAddress = "0x5D36dCdB35D0C85D88C5AA31E37cac165B480ba4"
	n.CreateTask(testutil.TestUser1(), tr3)

	result, err := n.ListTasksByUser(testutil.TestUser1(), &avsproto.ListTasksReq{
		SmartWalletAddress: []string{"0x5D36dCdB35D0C85D88C5AA31E37cac165B480ba4"},
	})

	if err != nil {
		t.Errorf("expect list task succesfully but got error %s", err)
	}

	if len(result.Items) != 1 {
		t.Errorf("list task return wrong. expect 1, got %d", len(result.Items))
	}
	if result.Items[0].Memo != "t3" {
		t.Errorf("list task return wrong. expect memo t1, got %s", result.Items[0].Memo)
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
	if result.Items[0].Memo != "t2" && result.Items[1].Memo != "t1" {
		t.Errorf("list task returns wrong data. expect t2, t1 got %s, %s", result.Items[0].Memo, result.Items[1].Memo)
	}
}

func TestListTasksPagination(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	n.CreateSmartWallet(testutil.TestUser1(), &avsproto.GetWalletReq{
		Salt: "12345",
	})
	n.CreateSmartWallet(testutil.TestUser1(), &avsproto.GetWalletReq{
		Salt: "6789",
	})

	// Firs we setup test for a 3 smart walets, with overlap ordering
	// Now create a test task
	tr1 := testutil.RestTask()
	tr1.Memo = "t1"
	// salt 0
	tr1.SmartWalletAddress = "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"
	n.CreateTask(testutil.TestUser1(), tr1)

	tr2 := testutil.RestTask()
	tr2.Memo = "t2_1"
	// salt 12345
	tr2.SmartWalletAddress = "0x961d2DD008960A9777571D78D21Ec9C3E5c6020c"
	n.CreateTask(testutil.TestUser1(), tr2)

	for i := range 20 {
		tr3 := testutil.RestTask()
		// salt 6789
		tr3.Memo = fmt.Sprintf("t3_%d", i)
		tr3.SmartWalletAddress = "0x5D36dCdB35D0C85D88C5AA31E37cac165B480ba4"
		n.CreateTask(testutil.TestUser1(), tr3)
	}

	tr2 = testutil.RestTask()
	tr2.Memo = "t2_2"
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
	if result.Items[0].Memo != "t2_2" {
		t.Errorf("list task returns first task wrong. expect task t2, got %s", result.Items[0].Memo)
	}

	if result.Items[2].Memo != "t3_18" || result.Items[4].Memo != "t3_16" {
		t.Errorf("list task returns wrong task result, expected t3_19 t3_17 got %s %s", result.Items[2].Memo, result.Items[4].Memo)
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
	if result.Items[0].Memo != "t3_15" || result.Items[2].Memo != "t3_13" || result.Items[14].Memo != "t3_1" {
		t.Errorf("list task returns wrong task result, expected t3_15 t3_13 t3_1 got %s %s %s", result.Items[0].Memo, result.Items[2].Memo, result.Items[14].Memo)
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
	if result.Items[0].Memo != "t3_0" || result.Items[1].Memo != "t2_1" {
		t.Errorf("list task returns wrong task result, expected t3_15 t3_1 got %s %s", result.Items[0].Memo, result.Items[1].Memo)
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
	tr1.Memo = "t1"
	// salt 0
	tr1.SmartWalletAddress = "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"
	result, _ := n.CreateTask(testutil.TestUser1(), tr1)

	resultTrigger, err := n.TriggerTask(testutil.TestUser1(), &avsproto.UserTriggerTaskReq{
		TaskId: result.Id,
		TriggerMetadata: &avsproto.TriggerMetadata{
			BlockNumber: 101,
		},
		IsBlocking: true,
	})

	// Now get back that exectuon id
	execution, err := n.GetExecution(testutil.TestUser1(), &avsproto.GetExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})

	if execution.Id != resultTrigger.ExecutionId {
		t.Errorf("invalid execution id. expect %s got %s", resultTrigger.ExecutionId, execution.Id)
	}

	if execution.TriggerMetadata.BlockNumber != 101 {
		t.Errorf("invalid triggered block. expect 101 got %d", execution.TriggerMetadata.BlockNumber)
	}

	// Another user cannot get this executin id
	execution, err = n.GetExecution(testutil.TestUser2(), &avsproto.GetExecutionReq{
		TaskId:      result.Id,
		ExecutionId: resultTrigger.ExecutionId,
	})
	if err == nil || execution != nil {
		t.Errorf("expected failure getting other user execution but succesfully read it")
	}
}
