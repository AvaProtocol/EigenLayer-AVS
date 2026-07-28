package taskengine

import (
	"math"
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

	_, err := n.CreateWorkflow(testutil.TestUser1(), tr1)

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

	_, err := n.CreateWorkflow(testutil.TestUser1(), tr1)

	if err == nil {
		t.Errorf("expect error when creating task with empty edges")
	}
}

func TestCreateTaskReturnErrorWhenInvalidBlockTriggerInterval(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	// A block interval only means anything in wall-clock, so the boundary is
	// derived from MinBlockTriggerInterval and the test chain's block time
	// rather than hardcoded — the cases stay correct if either constant moves.
	blockTime := blockTimeForChain(n.defaultChainID())
	minBlocks := int64(math.Ceil(float64(MinBlockTriggerInterval) / float64(blockTime)))

	testCases := []struct {
		name        string
		interval    int64
		wantErr     bool
		errContains string
	}{
		// Non-positive intervals are rejected before the frequency check, by
		// the proto→model conversion.
		{name: "zero", interval: 0, wantErr: true, errContains: "Invalid task argument"},
		{name: "negative", interval: -1, wantErr: true, errContains: "Invalid task argument"},
		// Every block was valid until the frequency floor landed: 7,200
		// executions/day on a 12s chain, and previously checked only for > 0.
		{name: "every block", interval: 1, wantErr: true, errContains: "faster than the"},
		{name: "one below the floor", interval: minBlocks - 1, wantErr: true, errContains: "faster than the"},
		{name: "exactly at the floor", interval: minBlocks, wantErr: false},
		{name: "well above the floor", interval: minBlocks * 20, wantErr: false},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			tr1 := testutil.RestTask()
			tr1.Trigger.TriggerType = &avsproto.TaskTrigger_Block{
				Block: &avsproto.BlockTrigger{
					Config: &avsproto.BlockTrigger_Config{
						Interval: tt.interval,
					},
				},
			}

			_, err := n.CreateWorkflow(testutil.TestUser1(), tr1)

			if !tt.wantErr {
				if err != nil {
					t.Errorf("interval %d (block time %s): unexpected error: %v", tt.interval, blockTime, err)
				}
				return
			}

			if err == nil {
				t.Fatalf("interval %d (block time %s): expected an error, got nil", tt.interval, blockTime)
			}
			if !strings.Contains(err.Error(), tt.errContains) {
				t.Errorf("interval %d: expected error containing %q, got: %v", tt.interval, tt.errContains, err)
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

	_, err := n.CreateWorkflow(testutil.TestUser1(), tr1)

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

	// Get three different wallets with different salts to derive different smart wallet addresses
	wallet1Resp, err := n.GetWallet(testutil.TestUser1(), &avsproto.GetWalletReq{
		Salt: "12345",
	})
	if err != nil {
		t.Fatalf("Failed to get wallet 1: %v", err)
	}
	smartWallet1Address := wallet1Resp.Address

	wallet2Resp, err := n.GetWallet(testutil.TestUser1(), &avsproto.GetWalletReq{
		Salt: "6789",
	})
	if err != nil {
		t.Fatalf("Failed to get wallet 2: %v", err)
	}
	smartWallet2Address := wallet2Resp.Address

	wallet3Resp, err := n.GetWallet(testutil.TestUser1(), &avsproto.GetWalletReq{
		Salt: "99999",
	})
	if err != nil {
		t.Fatalf("Failed to get wallet 3: %v", err)
	}
	smartWallet3Address := wallet3Resp.Address

	tr1 := testutil.RestTask()
	testutil.SetTaskSettings(tr1, "t1", smartWallet1Address)
	n.CreateWorkflow(testutil.TestUser1(), tr1)

	tr2 := testutil.RestTask()
	testutil.SetTaskSettings(tr2, "t2", smartWallet2Address)
	n.CreateWorkflow(testutil.TestUser1(), tr2)

	tr3 := testutil.RestTask()
	testutil.SetTaskSettings(tr3, "t3", smartWallet3Address)
	n.CreateWorkflow(testutil.TestUser1(), tr3)

	result, err := n.ListWorkflowsByUser(testutil.TestUser1(), &avsproto.ListTasksReq{
		SmartWalletAddress: []string{smartWallet3Address},
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

	result, err = n.ListWorkflowsByUser(testutil.TestUser1(), &avsproto.ListTasksReq{
		SmartWalletAddress: []string{
			smartWallet1Address,
			smartWallet2Address,
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

	_, err := n.CreateWorkflow(testutil.TestUser1(), tr1)

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

	_, err = n.CreateWorkflow(testutil.TestUser1(), tr2)

	if err != nil {
		t.Errorf("expect no error when creating task with valid expiration date, but got %s", err.Error())
	}

	// Test case 3: No expiration date (ExpiredAt = 0, should succeed)
	tr3 := testutil.RestTask()
	tr3.ExpiredAt = 0

	_, err = n.CreateWorkflow(testutil.TestUser1(), tr3)

	if err != nil {
		t.Errorf("expect no error when creating task with no expiration date, but got %s", err.Error())
	}
}
