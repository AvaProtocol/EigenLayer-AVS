package taskengine

import (
	"strings"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func TestCreateTaskReturnErrorWhenEmptyNodes(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	tr1 := testutil.RestTask()
	tr1.Nodes = []*avsproto.TaskNode{}

	_, err := n.CreateTask(testutil.TestUser1(), tr1)

	if err == nil {
		t.Errorf("expect error when creating task with empty nodes")
	}

	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expect error message to contain 'invalid' but got %s", err.Error())
	}
}

func TestCreateTaskReturnErrorWhenEmptyEdges(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	tr1 := testutil.RestTask()
	tr1.Edges = []*avsproto.TaskEdge{}

	_, err := n.CreateTask(testutil.TestUser1(), tr1)

	if err == nil {
		t.Errorf("expect error when creating task with empty edges")
	}
}

func TestCreateTaskReturnErrorWhenInvalidBlockTriggerInterval(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	testCases := []struct {
		interval int64
		wantErr  bool
	}{
		{interval: 0, wantErr: true},
		{interval: -1, wantErr: true},
		{interval: 1, wantErr: false},
		{interval: 100, wantErr: false},
		{interval: 1000, wantErr: false},
	}

	for _, tt := range testCases {
		t.Run("", func(t *testing.T) {
			tr1 := testutil.RestTask()
			tr1.Trigger.TriggerType = &avsproto.TaskTrigger_Block{
				Block: &avsproto.BlockTrigger{
					Config: &avsproto.BlockTrigger_Config{
						Interval: tt.interval,
					},
				},
			}

			_, err := n.CreateTask(testutil.TestUser1(), tr1)

			if !tt.wantErr && err != nil {
				t.Errorf("CreateTask() unexpected error for interval %d: %v", tt.interval, err)
			}

			if tt.wantErr && err != nil {
				t.Logf("CreateTask() correctly rejected interval %d with error: %v", tt.interval, err)
				if !strings.Contains(err.Error(), "Invalid task argument") {
					t.Errorf("Expected error to contain 'Invalid task argument', got: %v", err)
				}
			}
		})
	}
}

func TestCreateTaskReturnErrorWhenNilBlockTriggerConfig(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	tr1 := testutil.RestTask()
	tr1.Trigger.TriggerType = &avsproto.TaskTrigger_Block{
		Block: &avsproto.BlockTrigger{
			Config: nil, // This should cause validation to fail
		},
	}

	_, err := n.CreateTask(testutil.TestUser1(), tr1)

	if err == nil {
		t.Error("CreateTask() expected error for nil block trigger config, but got none")
	}

	if err != nil {
		t.Logf("CreateTask() correctly rejected nil config with error: %v", err)
		if !strings.Contains(err.Error(), "block trigger config is required but missing") {
			t.Errorf("Expected error to contain 'block trigger config is required but missing', got: %v", err)
		}
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

	tr1 := testutil.RestTask()
	tr1.Name = "t1"
	tr1.SmartWalletAddress = "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"
	n.CreateTask(testutil.TestUser1(), tr1)

	tr2 := testutil.RestTask()
	tr2.Name = "t2"
	tr2.SmartWalletAddress = "0x961d2DD008960A9777571D78D21Ec9C3E5c6020c"
	n.CreateTask(testutil.TestUser1(), tr2)

	tr3 := testutil.RestTask()
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

func TestCreateTaskReturnErrorWhenExpirationTooClose(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	// Test case 1: Expiration date 30 minutes from now (should fail)
	tr1 := testutil.RestTask()
	now := time.Now()
	expiredAtTooClose := now.Add(30*time.Minute).Unix() * 1000 // 30 minutes from now in milliseconds
	tr1.ExpiredAt = expiredAtTooClose

	_, err := n.CreateTask(testutil.TestUser1(), tr1)

	if err == nil {
		t.Errorf("expect error when creating task with expiration date too close to current time")
	}

	if !strings.Contains(err.Error(), "too close to current time") {
		t.Errorf("expect error message to contain 'too close to current time' but got %s", err.Error())
	}

	// Test case 2: Expiration date 2 hours from now (should succeed)
	tr2 := testutil.RestTask()
	expiredAtValid := now.Add(2*time.Hour).Unix() * 1000 // 2 hours from now in milliseconds
	tr2.ExpiredAt = expiredAtValid

	_, err = n.CreateTask(testutil.TestUser1(), tr2)

	if err != nil {
		t.Errorf("expect no error when creating task with valid expiration date, but got %s", err.Error())
	}

	// Test case 3: No expiration date (ExpiredAt = 0, should succeed)
	tr3 := testutil.RestTask()
	tr3.ExpiredAt = 0

	_, err = n.CreateTask(testutil.TestUser1(), tr3)

	if err != nil {
		t.Errorf("expect no error when creating task with no expiration date, but got %s", err.Error())
	}
}
