package taskengine

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
)

func TestTaskStatCount(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	// Get a wallet for the user to derive the correct smart wallet address
	walletResp, err := n.GetWallet(testutil.TestUser1(), &avsproto.GetWalletReq{
		Salt: "12345",
	})
	if err != nil {
		t.Fatalf("Failed to get wallet: %v", err)
	}
	smartWalletAddress := walletResp.Address

	// Populate task
	tr1 := testutil.RestTask()
	tr1.Name = "t1"
	tr1.MaxExecution = 1
	tr1.SmartWalletAddress = smartWalletAddress
	n.CreateTask(testutil.TestUser1(), tr1)

	statSvc := NewStatService(db)
	// Query statistics using the same smart wallet address used for task creation
	addr := common.HexToAddress(smartWalletAddress)
	owner := testutil.TestUser1().Address
	result, _ := statSvc.GetTaskCount(&model.SmartWallet{
		Owner:   &owner,
		Address: &addr,
	})

	if !reflect.DeepEqual(
		result, &model.SmartWalletTaskStat{
			Total:  1,
			Active: 1,
		}) {
		t.Errorf("expect task total is 1, but got %v", result)
	}
}

func TestTaskStatCountCompleted(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	user1 := testutil.TestUser1()

	task1 := &model.Task{
		Task: &avsproto.Task{
			Owner:              user1.Address.Hex(),
			SmartWalletAddress: user1.SmartAccountAddress.Hex(),
			Id:                 "t1",
		},
	}

	db.Set(TaskUserKey(task1), []byte(fmt.Sprintf("%d", avsproto.TaskStatus_Completed)))

	statSvc := NewStatService(db)
	result, _ := statSvc.GetTaskCount(user1.ToSmartWallet())

	if !reflect.DeepEqual(
		result, &model.SmartWalletTaskStat{
			Total:     1,
			Active:    0,
			Completed: 1,
		}) {
		t.Errorf("expect task total is 1, completed is 1 but got %v", result)
	}
}

func TestTaskStatCountAllStatus(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	user1 := testutil.TestUser1()

	task1 := &model.Task{
		Task: &avsproto.Task{
			Owner:              user1.Address.Hex(),
			SmartWalletAddress: user1.SmartAccountAddress.Hex(),
			Id:                 "t1",
		},
	}

	task2 := &model.Task{
		Task: &avsproto.Task{
			Owner:              user1.Address.Hex(),
			SmartWalletAddress: user1.SmartAccountAddress.Hex(),
			Id:                 "t2",
		},
	}

	task3 := &model.Task{
		Task: &avsproto.Task{
			Owner:              user1.Address.Hex(),
			SmartWalletAddress: user1.SmartAccountAddress.Hex(),
			Id:                 "t3",
		},
	}

	task4 := &model.Task{
		Task: &avsproto.Task{
			Owner:              user1.Address.Hex(),
			SmartWalletAddress: user1.SmartAccountAddress.Hex(),
			Id:                 "t4",
		},
	}

	db.Set(TaskUserKey(task1), []byte(fmt.Sprintf("%d", avsproto.TaskStatus_Completed)))
	db.Set(TaskUserKey(task2), []byte(fmt.Sprintf("%d", avsproto.TaskStatus_Failed)))
	db.Set(TaskUserKey(task3), []byte(fmt.Sprintf("%d", avsproto.TaskStatus_Disabled)))
	db.Set(TaskUserKey(task4), []byte(fmt.Sprintf("%d", avsproto.TaskStatus_Enabled)))

	statSvc := NewStatService(db)
	result, _ := statSvc.GetTaskCount(user1.ToSmartWallet())

	if !reflect.DeepEqual(
		result, &model.SmartWalletTaskStat{
			Total:     4,
			Active:    1,
			Completed: 1,
			Failed:    1,
			Inactive:  1,
		}) {
		t.Errorf("expect task total=4, active=1, completed=1, failed=1, inactive=1, but got %v", result)
	}

}
