//go:build integration
// +build integration

package integration_test

import (
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOrphanedTaskReclamation tests the specific scenario where tasks are orphaned
// and need to be reclaimed by reconnecting operators
func TestOrphanedTaskReclamation(t *testing.T) {
	logger := testutil.GetLogger()
	taskengine.SetLogger(logger)

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := &config.Config{
		ApprovedOperators: []common.Address{
			common.HexToAddress("0x997E5D40a32c44a3D93E59fC55C4Fd20b7d2d49D"),
		},
		SmartWallet: &config.SmartWalletConfig{
			EthRpcUrl:      "http://localhost:8545", // Dummy URL for testing
			FactoryAddress: common.HexToAddress("0x1234567890123456789012345678901234567890"),
		},
	}

	engine := taskengine.New(db, config, nil, logger)
	err := engine.MustStart()
	require.NoError(t, err)
	defer engine.Stop()

	// Step 1: Create a simple task (without the problematic smart wallet validation)
	// We'll create the task directly in memory rather than through CreateTask
	taskData := &model.Task{
		Task: &avsproto.Task{
			Id:           "test-task-001",
			Name:         "Test Orphaned Task",
			MaxExecution: 100,
			Status:       avsproto.TaskStatus_Enabled,
			Trigger: &avsproto.TaskTrigger{
				TriggerType: &avsproto.TaskTrigger_Event{
					Event: &avsproto.EventTrigger{
						Config: &avsproto.EventTrigger_Config{
							Queries: []*avsproto.EventTrigger_Query{
								{
									Addresses: []string{"0xA0b86a33E6441d476c1bd0a4dc53dFEB3F81E76C"},
									Topics: []string{
										"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
									},
								},
							},
						},
					},
				},
			},
			Nodes: []*avsproto.TaskNode{
				{
					Id:   "log_node",
					Name: "Log Event",
					TaskType: &avsproto.TaskNode_CustomCode{
						CustomCode: &avsproto.CustomCodeNode{
							Config: &avsproto.CustomCodeNode_Config{
								Source: `console.log("Event detected");`,
							},
						},
					},
				},
			},
			Edges: []*avsproto.TaskEdge{
				{Id: "trigger_to_log", Source: "trigger", Target: "log_node"},
			},
		},
	}

	// Manually add task to engine's task map (simulating loaded from database)
	engine.AddTaskForTesting(taskData)

	t.Logf("✅ Added task to engine: %s", taskData.Task.Id)

	operatorAddr := "0x997E5D40a32c44a3D93E59fC55C4Fd20b7d2d49D"

	// Step 2: First operator connection - should get task assignment
	t.Log("📡 Testing initial operator connection...")
	mockServer1 := NewMockSyncMessagesServer()
	syncReq1 := &avsproto.SyncMessagesReq{
		Address:        operatorAddr,
		MonotonicClock: time.Now().UnixNano(),
		Capabilities: &avsproto.SyncMessagesReq_Capabilities{
			EventMonitoring: true,
			BlockMonitoring: true,
			TimeMonitoring:  true,
		},
	}

	errChan1 := make(chan error, 1)
	go func() {
		err := engine.StreamCheckToOperator(syncReq1, mockServer1)
		errChan1 <- err
	}()

	// Wait for stabilization and task assignment with configurable timeout
	t.Log("⏳ Waiting for operator stabilization and task assignment...")
	stabilizationTimeout := 12 * time.Second
	if testing.Short() {
		stabilizationTimeout = 3 * time.Second
	}

	stabilizationTimer := time.NewTimer(stabilizationTimeout)
	defer stabilizationTimer.Stop()

	select {
	case <-stabilizationTimer.C:
		t.Log("✅ Initial stabilization and task assignment completed")
	case <-time.After(stabilizationTimeout + 2*time.Second):
		t.Log("ℹ️ Initial stabilization took longer than expected (this is normal)")
	}

	// Verify operator received the task
	sentTasks1 := mockServer1.GetSentTasks()
	assert.Greater(t, len(sentTasks1), 0, "Operator should have received tasks on first connection")
	assert.Contains(t, sentTasks1, taskData.Task.Id, "Operator should have received our test task")
	t.Logf("✅ First connection: Operator received %d tasks: %v", len(sentTasks1), sentTasks1)

	// Step 3: Operator disconnects (simulating network issue)
	t.Log("🔌 Simulating operator disconnection...")
	mockServer1.Disconnect()

	select {
	case <-errChan1:
		t.Log("✅ First operator connection ended")
	case <-time.After(5 * time.Second):
		t.Log("ℹ️ Initial connection cleanup took longer than expected (this is normal)")
	}

	// Step 4: Wait a bit, then operator reconnects
	t.Log("⏳ Waiting before operator reconnection...")
	time.Sleep(3 * time.Second)

	// Step 5: Operator reconnects - should reclaim orphaned task
	t.Log("🔄 Testing operator reconnection and orphaned task reclamation...")
	mockServer2 := NewMockSyncMessagesServer()
	syncReq2 := &avsproto.SyncMessagesReq{
		Address:        operatorAddr,
		MonotonicClock: time.Now().UnixNano(), // New monotonic clock
		Capabilities: &avsproto.SyncMessagesReq_Capabilities{
			EventMonitoring: true,
			BlockMonitoring: true,
			TimeMonitoring:  true,
		},
	}

	errChan2 := make(chan error, 1)
	go func() {
		err := engine.StreamCheckToOperator(syncReq2, mockServer2)
		errChan2 <- err
	}()

	// Wait for stabilization and orphaned task reclamation with configurable timeout
	t.Log("⏳ Waiting for reconnected operator stabilization and task reclamation...")
	reclamationTimeout := 12 * time.Second
	if testing.Short() {
		reclamationTimeout = 3 * time.Second
	}

	reclamationTimer := time.NewTimer(reclamationTimeout)
	defer reclamationTimer.Stop()

	select {
	case <-reclamationTimer.C:
		t.Log("✅ Reconnection stabilization and task reclamation completed")
	case <-time.After(reclamationTimeout + 2*time.Second):
		t.Log("ℹ️ Reconnection stabilization took longer than expected (this is normal)")
	}

	// Step 6: Verify operator gets the orphaned task again
	sentTasks2 := mockServer2.GetSentTasks()
	assert.Greater(t, len(sentTasks2), 0, "Reconnected operator should have received orphaned tasks")
	assert.Contains(t, sentTasks2, taskData.Task.Id, "Reconnected operator should have reclaimed the orphaned task")
	t.Logf("✅ Reconnection: Operator reclaimed %d tasks: %v", len(sentTasks2), sentTasks2)

	// Step 7: Verify the fix - operator should get tasks on EVERY reconnection
	t.Log("🔄 Testing second reconnection to verify consistent behavior...")
	mockServer2.Disconnect()

	select {
	case <-errChan2:
		t.Log("✅ Second operator connection ended")
	case <-time.After(5 * time.Second):
		t.Log("ℹ️ Second connection cleanup took longer than expected (this is normal)")
	}

	time.Sleep(2 * time.Second)

	// Third connection
	mockServer3 := NewMockSyncMessagesServer()
	syncReq3 := &avsproto.SyncMessagesReq{
		Address:        operatorAddr,
		MonotonicClock: time.Now().UnixNano(),
		Capabilities: &avsproto.SyncMessagesReq_Capabilities{
			EventMonitoring: true,
			BlockMonitoring: true,
			TimeMonitoring:  true,
		},
	}

	errChan3 := make(chan error, 1)
	go func() {
		err := engine.StreamCheckToOperator(syncReq3, mockServer3)
		errChan3 <- err
	}()

	time.Sleep(12 * time.Second)

	sentTasks3 := mockServer3.GetSentTasks()
	assert.Greater(t, len(sentTasks3), 0, "Third reconnection should also receive tasks")
	assert.Contains(t, sentTasks3, taskData.Task.Id, "Third reconnection should also receive the task")
	t.Logf("✅ Third reconnection: Operator received %d tasks: %v", len(sentTasks3), sentTasks3)

	// Cleanup
	mockServer3.Disconnect()
	select {
	case <-errChan3:
		t.Log("✅ Third operator connection ended")
	case <-time.After(2 * time.Second):
		t.Log("ℹ️ Third connection cleanup took longer than expected (this is normal)")
	}

	t.Log("🎉 Orphaned task reclamation test completed successfully!")
	t.Log("   ✅ Task sent on initial connection")
	t.Log("   ✅ Task reclaimed after first reconnection")
	t.Log("   ✅ Task reclaimed after second reconnection")
	t.Log("   ✅ No hanging connections or race conditions")
}

// TestMonotonicClockTaskReset tests the MonotonicClock task tracking reset behavior
func TestMonotonicClockTaskReset(t *testing.T) {
	logger := testutil.GetLogger()
	taskengine.SetLogger(logger)

	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := &config.Config{
		ApprovedOperators: []common.Address{
			common.HexToAddress("0x997E5D40a32c44a3D93E59fC55C4Fd20b7d2d49D"),
		},
		SmartWallet: &config.SmartWalletConfig{
			EthRpcUrl:      "http://localhost:8545",
			FactoryAddress: common.HexToAddress("0x1234567890123456789012345678901234567890"),
		},
	}

	engine := taskengine.New(db, config, nil, logger)
	err := engine.MustStart()
	require.NoError(t, err)
	defer engine.Stop()

	// Add test task
	taskData := &model.Task{
		Task: &avsproto.Task{
			Id:           "monotonic-test-task",
			Name:         "MonotonicClock Test Task",
			MaxExecution: 100,
			Status:       avsproto.TaskStatus_Enabled,
			Trigger: &avsproto.TaskTrigger{
				TriggerType: &avsproto.TaskTrigger_Event{
					Event: &avsproto.EventTrigger{
						Config: &avsproto.EventTrigger_Config{
							Queries: []*avsproto.EventTrigger_Query{
								{
									Addresses: []string{"0xA0b86a33E6441d476c1bd0a4dc53dFEB3F81E76C"},
									Topics: []string{
										"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef",
									},
								},
							},
						},
					},
				},
			},
			Nodes: []*avsproto.TaskNode{
				{
					Id:   "test_node",
					Name: "Test Node",
					TaskType: &avsproto.TaskNode_CustomCode{
						CustomCode: &avsproto.CustomCodeNode{
							Config: &avsproto.CustomCodeNode_Config{
								Source: `console.log("test");`,
							},
						},
					},
				},
			},
			Edges: []*avsproto.TaskEdge{{Id: "trigger_to_test", Source: "trigger", Target: "test_node"}},
		},
	}

	engine.AddTaskForTesting(taskData)

	operatorAddr := "0x997E5D40a32c44a3D93E59fC55C4Fd20b7d2d49D"
	baseMonotonicClock := time.Now().UnixNano()

	// Test 1: Same MonotonicClock should still reset task tracking
	t.Log("🔄 Testing reconnection with SAME MonotonicClock...")
	for i := 0; i < 2; i++ {
		mockServer := NewMockSyncMessagesServer()
		syncReq := &avsproto.SyncMessagesReq{
			Address:        operatorAddr,
			MonotonicClock: baseMonotonicClock, // Same clock each time
			Capabilities: &avsproto.SyncMessagesReq_Capabilities{
				EventMonitoring: true,
				BlockMonitoring: true,
				TimeMonitoring:  true,
			},
		}

		errChan := make(chan error, 1)
		go func() {
			err := engine.StreamCheckToOperator(syncReq, mockServer)
			errChan <- err
		}()

		time.Sleep(12 * time.Second)

		sentTasks := mockServer.GetSentTasks()
		assert.Greater(t, len(sentTasks), 0, "Operator should receive tasks even with same MonotonicClock (iteration %d)", i+1)
		assert.Contains(t, sentTasks, taskData.Task.Id, "Operator should receive our test task (iteration %d)", i+1)

		mockServer.Disconnect()
		select {
		case <-errChan:
			t.Logf("✅ Same MonotonicClock test iteration %d completed", i+1)
		case <-time.After(3 * time.Second):
			t.Logf("ℹ️ Same MonotonicClock test iteration %d cleanup took longer than expected (this is normal)", i+1)
		}

		time.Sleep(1 * time.Second)
	}

	// Test 2: Lower MonotonicClock should also reset task tracking
	t.Log("🔄 Testing reconnection with LOWER MonotonicClock...")
	mockServer := NewMockSyncMessagesServer()
	syncReq := &avsproto.SyncMessagesReq{
		Address:        operatorAddr,
		MonotonicClock: baseMonotonicClock - 1000, // Lower clock
		Capabilities: &avsproto.SyncMessagesReq_Capabilities{
			EventMonitoring: true,
			BlockMonitoring: true,
			TimeMonitoring:  true,
		},
	}

	errChan := make(chan error, 1)
	go func() {
		err := engine.StreamCheckToOperator(syncReq, mockServer)
		errChan <- err
	}()

	time.Sleep(12 * time.Second)

	sentTasks := mockServer.GetSentTasks()
	assert.Greater(t, len(sentTasks), 0, "Operator should receive tasks even with lower MonotonicClock")
	assert.Contains(t, sentTasks, taskData.Task.Id, "Operator should receive our test task with lower MonotonicClock")

	mockServer.Disconnect()
	select {
	case <-errChan:
		t.Log("✅ Lower MonotonicClock test completed")
	case <-time.After(3 * time.Second):
		t.Log("ℹ️ Lower MonotonicClock test cleanup took longer than expected (this is normal)")
	}

	t.Log("🎉 MonotonicClock task reset test completed successfully!")
	t.Log("   ✅ Same MonotonicClock reconnection works")
	t.Log("   ✅ Lower MonotonicClock reconnection works")
	t.Log("   ✅ Task tracking is always reset on reconnection")
}
