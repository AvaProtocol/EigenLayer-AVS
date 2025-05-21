package taskengine

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func TestTaskStatCount(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	user1 := testutil.TestUser1()

	// Populate task
	tr1 := testutil.RestTask()
	tr1.Name = "t1"
	tr1.MaxExecution = 1
	// salt 0
	tr1.SmartWalletAddress = "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"
	n.CreateTask(testutil.TestUser1(), tr1)

	statSvc := NewStatService(db)
	result, _ := statSvc.GetTaskCount(user1.ToSmartWallet())

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
	db.Set(TaskUserKey(task3), []byte(fmt.Sprintf("%d", avsproto.TaskStatus_Canceled)))
	db.Set(TaskUserKey(task4), []byte(fmt.Sprintf("%d", avsproto.TaskStatus_Active)))

	statSvc := NewStatService(db)
	result, _ := statSvc.GetTaskCount(user1.ToSmartWallet())

	if !reflect.DeepEqual(
		result, &model.SmartWalletTaskStat{
			Total:     4,
			Active:    1,
			Completed: 1,
			Failed:    1,
			Canceled:  1,
		}) {
		t.Errorf("expect task total=4, active=1, completed=1, failed=1, canceled=1, but got %v", result)
	}

}
