package taskengine

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc/metadata"
)

// MockSyncMessagesServer implements avsproto.Node_SyncMessagesServer for testing
type MockSyncMessagesServer struct {
	mock.Mock
	receivedMessages []*avsproto.SyncMessagesResp
	messageTypes     []avsproto.MessageOp
	mutex            sync.Mutex
}

func (m *MockSyncMessagesServer) Send(resp *avsproto.SyncMessagesResp) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	m.receivedMessages = append(m.receivedMessages, resp)
	m.messageTypes = append(m.messageTypes, resp.Op)
	return nil
}

func (m *MockSyncMessagesServer) Context() context.Context {
	return context.Background()
}

func (m *MockSyncMessagesServer) SetHeader(metadata.MD) error  { return nil }
func (m *MockSyncMessagesServer) SendHeader(metadata.MD) error { return nil }
func (m *MockSyncMessagesServer) SetTrailer(metadata.MD)       {}
func (m *MockSyncMessagesServer) RecvMsg(interface{}) error    { return nil }
func (m *MockSyncMessagesServer) SendMsg(interface{}) error    { return nil }

func (m *MockSyncMessagesServer) GetReceivedMessages() []*avsproto.SyncMessagesResp {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	result := make([]*avsproto.SyncMessagesResp, len(m.receivedMessages))
	copy(result, m.receivedMessages)
	return result
}

func (m *MockSyncMessagesServer) GetMessageTypes() []avsproto.MessageOp {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	result := make([]avsproto.MessageOp, len(m.messageTypes))
	copy(result, m.messageTypes)
	return result
}

func TestNotifyOperatorsTaskOperation_DeleteTask(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// Must start the engine to load tasks
	err := engine.MustStart()
	assert.NoError(t, err)

	user := testutil.TestUser1()

	// Create a test task
	taskReq := testutil.RestTask()
	taskReq.SmartWalletAddress = user.SmartAccountAddress.Hex()
	task, err := engine.CreateTask(user, taskReq)
	assert.NoError(t, err)

	// Set up mock operator stream
	mockStream := &MockSyncMessagesServer{}
	operatorAddr := "0x1234567890123456789012345678901234567890"

	// Manually register the operator stream and track the task
	engine.streamsMutex.Lock()
	engine.operatorStreams[operatorAddr] = mockStream
	engine.streamsMutex.Unlock()

	engine.lock.Lock()
	if engine.trackSyncedTasks[operatorAddr] == nil {
		engine.trackSyncedTasks[operatorAddr] = &operatorState{
			TaskID: make(map[string]bool),
		}
	}
	engine.trackSyncedTasks[operatorAddr].TaskID[task.Id] = true
	engine.lock.Unlock()

	// Delete the task (this should trigger notification)
	deleted, err := engine.DeleteTaskByUser(user, task.Id)
	assert.NoError(t, err)
	assert.True(t, deleted.Success)

	// Manually trigger batch processing for immediate testing
	engine.sendBatchedNotifications()

	// Give some time for async notification processing
	time.Sleep(100 * time.Millisecond)

	// Verify the notification was sent
	messages := mockStream.GetReceivedMessages()
	messageTypes := mockStream.GetMessageTypes()

	assert.Len(t, messages, 1, "Should have received exactly one notification")
	assert.Equal(t, avsproto.MessageOp_DeleteTask, messageTypes[0], "Should have received DeleteTask notification")
	assert.Equal(t, task.Id, messages[0].Id, "Notification should contain correct task ID")
}

func TestNotifyOperatorsTaskOperation_DeactivateTask(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// Must start the engine to load tasks
	err := engine.MustStart()
	assert.NoError(t, err)

	user := testutil.TestUser1()

	// Create a test task
	taskReq := testutil.RestTask()
	taskReq.SmartWalletAddress = user.SmartAccountAddress.Hex()
	task, err := engine.CreateTask(user, taskReq)
	assert.NoError(t, err)

	// Set up mock operator stream
	mockStream := &MockSyncMessagesServer{}
	operatorAddr := "0x1234567890123456789012345678901234567890"

	// Manually register the operator stream and track the task
	engine.streamsMutex.Lock()
	engine.operatorStreams[operatorAddr] = mockStream
	engine.streamsMutex.Unlock()

	engine.lock.Lock()
	if engine.trackSyncedTasks[operatorAddr] == nil {
		engine.trackSyncedTasks[operatorAddr] = &operatorState{
			TaskID: make(map[string]bool),
		}
	}
	engine.trackSyncedTasks[operatorAddr].TaskID[task.Id] = true
	engine.lock.Unlock()

	// Disable the task (this should trigger notification)
	resp, err := engine.SetTaskEnabledByUser(user, task.Id, false)
	assert.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, "disabled", resp.Status)

	// Manually trigger batch processing for immediate testing
	engine.sendBatchedNotifications()

	// Give some time for async notification processing
	time.Sleep(100 * time.Millisecond)

	// Verify the notification was sent
	messages := mockStream.GetReceivedMessages()
	messageTypes := mockStream.GetMessageTypes()

	assert.Len(t, messages, 1, "Should have received exactly one notification")
	assert.Equal(t, avsproto.MessageOp_DisableTask, messageTypes[0], "Should have received DisableTask notification")
	assert.Equal(t, task.Id, messages[0].Id, "Notification should contain correct task ID")
}

func TestNotifyOperatorsTaskOperation_OnlyNotifiesTrackingOperators(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// Must start the engine to load tasks
	err := engine.MustStart()
	assert.NoError(t, err)

	user := testutil.TestUser1()

	// Create a test task
	taskReq := testutil.RestTask()
	taskReq.SmartWalletAddress = user.SmartAccountAddress.Hex()
	task, err := engine.CreateTask(user, taskReq)
	assert.NoError(t, err)

	// Set up two mock operator streams
	trackingStream := &MockSyncMessagesServer{}
	nonTrackingStream := &MockSyncMessagesServer{}

	trackingOperator := "0x1111111111111111111111111111111111111111"
	nonTrackingOperator := "0x2222222222222222222222222222222222222222"

	// Register both operator streams
	engine.streamsMutex.Lock()
	engine.operatorStreams[trackingOperator] = trackingStream
	engine.operatorStreams[nonTrackingOperator] = nonTrackingStream
	engine.streamsMutex.Unlock()

	// Only the first operator tracks this task
	engine.lock.Lock()
	engine.trackSyncedTasks[trackingOperator] = &operatorState{
		TaskID: map[string]bool{task.Id: true},
	}
	engine.trackSyncedTasks[nonTrackingOperator] = &operatorState{
		TaskID: make(map[string]bool), // Empty - not tracking our task
	}
	engine.lock.Unlock()

	// Delete the task
	deleted, err := engine.DeleteTaskByUser(user, task.Id)
	assert.NoError(t, err)
	assert.True(t, deleted.Success)

	// Manually trigger batch processing for immediate testing
	engine.sendBatchedNotifications()

	// Give some time for async notification processing
	time.Sleep(100 * time.Millisecond)

	// Verify only the tracking operator received notification
	trackingMessages := trackingStream.GetReceivedMessages()
	nonTrackingMessages := nonTrackingStream.GetReceivedMessages()

	assert.Len(t, trackingMessages, 1, "Tracking operator should receive notification")
	assert.Len(t, nonTrackingMessages, 0, "Non-tracking operator should not receive notification")

	assert.Equal(t, avsproto.MessageOp_DeleteTask, trackingMessages[0].Op)
	assert.Equal(t, task.Id, trackingMessages[0].Id)
}

func TestNotifyOperatorsTaskOperation_LockDuration(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// Create multiple mock streams to test lock contention
	numOperators := 10
	operators := make([]string, numOperators)
	streams := make([]*MockSyncMessagesServer, numOperators)

	engine.streamsMutex.Lock()
	for i := 0; i < numOperators; i++ {
		operators[i] = fmt.Sprintf("0x%040d", i)
		streams[i] = &MockSyncMessagesServer{}
		engine.operatorStreams[operators[i]] = streams[i]

		engine.trackSyncedTasks[operators[i]] = &operatorState{
			TaskID: map[string]bool{"test-task": true},
		}
	}
	engine.streamsMutex.Unlock()

	// Test that notification doesn't block new stream registrations
	var wg sync.WaitGroup

	// Start notification process
	wg.Add(1)
	go func() {
		defer wg.Done()
		engine.notifyOperatorsTaskOperation("test-task", avsproto.MessageOp_DeleteTask)
	}()

	// Simultaneously try to register new operator (should not block)
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(10 * time.Millisecond) // Small delay to ensure notification starts first

		newStream := &MockSyncMessagesServer{}
		engine.streamsMutex.Lock()
		engine.operatorStreams["0x9999999999999999999999999999999999999999"] = newStream
		engine.streamsMutex.Unlock()
	}()

	// Wait for both operations to complete (should not deadlock)
	done := make(chan bool)
	go func() {
		wg.Wait()
		done <- true
	}()

	select {
	case <-done:
		t.Log("✅ Lock contention test passed - no deadlock")
	case <-time.After(5 * time.Second):
		t.Fatal("❌ Operations blocked - potential lock contention issue")
	}

	// Manually trigger batch processing for immediate testing
	engine.sendBatchedNotifications()

	// Give some time for async notification processing
	time.Sleep(100 * time.Millisecond)

	// Verify all operators received notifications
	for i, stream := range streams {
		messages := stream.GetReceivedMessages()
		assert.Len(t, messages, 1, fmt.Sprintf("Operator %d should receive notification", i))
	}
}

func TestDeleteTaskRespFields(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	_ = engine.MustStart()
	user := testutil.TestUser1()

	taskReq := testutil.RestTask()
	taskReq.SmartWalletAddress = user.SmartAccountAddress.Hex()
	task, err := engine.CreateTask(user, taskReq)
	assert.NoError(t, err)

	resp, err := engine.DeleteTaskByUser(user, task.Id)
	assert.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, "deleted", resp.Status)
	assert.Equal(t, task.Id, resp.Id)
	assert.NotEmpty(t, resp.Message)
	assert.NotZero(t, resp.DeletedAt)
	assert.Equal(t, "Enabled", resp.PreviousStatus) // Task is enabled before deletion
}

func TestSetTaskEnabledRespFields(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	_ = engine.MustStart()
	user := testutil.TestUser1()

	taskReq := testutil.RestTask()
	taskReq.SmartWalletAddress = user.SmartAccountAddress.Hex()
	task, err := engine.CreateTask(user, taskReq)
	assert.NoError(t, err)

	// Disable
	resp, err := engine.SetTaskEnabledByUser(user, task.Id, false)
	assert.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, "disabled", resp.Status)
	assert.Equal(t, task.Id, resp.Id)
	assert.NotEmpty(t, resp.Message)
	assert.NotZero(t, resp.UpdatedAt)
	assert.Equal(t, "Enabled", resp.PreviousStatus) // Task is enabled before disabling
}

// TestReEnableTaskSendsMonitorNotification verifies that re-enabling a disabled task
// sends a MonitorTaskTrigger notification to operators immediately instead of waiting
// for the next StreamCheckToOperator ticker cycle.
func TestReEnableTaskSendsMonitorNotification(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	err := engine.MustStart()
	assert.NoError(t, err)

	user := testutil.TestUser1()

	// Create a test task
	taskReq := testutil.RestTask()
	taskReq.SmartWalletAddress = user.SmartAccountAddress.Hex()
	task, err := engine.CreateTask(user, taskReq)
	assert.NoError(t, err)

	// Use an approved operator address so CanStreamCheck returns true
	operatorAddr := "0x997e5d40a32c44a3d93e59fc55c4fd20b7d2d49d"
	mockStream := &MockSyncMessagesServer{}

	engine.streamsMutex.Lock()
	engine.operatorStreams[operatorAddr] = mockStream
	engine.streamsMutex.Unlock()

	engine.lock.Lock()
	engine.trackSyncedTasks[operatorAddr] = &operatorState{
		TaskID: map[string]bool{task.Id: true},
	}
	engine.lock.Unlock()

	// Step 1: Disable the task
	disableResp, err := engine.SetTaskEnabledByUser(user, task.Id, false)
	assert.NoError(t, err)
	assert.True(t, disableResp.Success)
	assert.Equal(t, "disabled", disableResp.Status)

	// Flush batched disable notification
	engine.sendBatchedNotifications()
	time.Sleep(100 * time.Millisecond)

	// Verify DisableTask was sent
	messages := mockStream.GetReceivedMessages()
	assert.Len(t, messages, 1, "Should have received DisableTask notification")
	assert.Equal(t, avsproto.MessageOp_DisableTask, messages[0].Op)
	assert.Equal(t, task.Id, messages[0].Id)

	// Step 2: Re-enable the task
	enableResp, err := engine.SetTaskEnabledByUser(user, task.Id, true)
	assert.NoError(t, err)
	assert.True(t, enableResp.Success)
	assert.Equal(t, "enabled", enableResp.Status)
	assert.Equal(t, "Disabled", enableResp.PreviousStatus)

	// Give the direct stream send time to complete
	time.Sleep(100 * time.Millisecond)

	// Verify MonitorTaskTrigger was sent immediately (without waiting for ticker)
	messages = mockStream.GetReceivedMessages()
	assert.Len(t, messages, 2, "Should have received DisableTask + MonitorTaskTrigger")
	assert.Equal(t, avsproto.MessageOp_MonitorTaskTrigger, messages[1].Op)
	assert.Equal(t, task.Id, messages[1].Id)
	// Verify task metadata is populated
	assert.NotNil(t, messages[1].TaskMetadata, "MonitorTaskTrigger should include TaskMetadata")
	assert.Equal(t, task.Id, messages[1].TaskMetadata.TaskId)
	assert.Equal(t, int64(1000), messages[1].TaskMetadata.Remain)
}

// TestReEnableTaskRoundTrip verifies that a task can go through the full
// enable -> disable -> re-enable cycle and its data is preserved.
func TestReEnableTaskRoundTrip(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	err := engine.MustStart()
	assert.NoError(t, err)

	user := testutil.TestUser1()

	// Create task
	taskReq := testutil.RestTask()
	taskReq.SmartWalletAddress = user.SmartAccountAddress.Hex()
	task, err := engine.CreateTask(user, taskReq)
	assert.NoError(t, err)
	originalID := task.Id

	// Disable
	disableResp, err := engine.SetTaskEnabledByUser(user, originalID, false)
	assert.NoError(t, err)
	assert.True(t, disableResp.Success)
	assert.Equal(t, "disabled", disableResp.Status)

	// Verify task is no longer in active tasks map
	engine.lock.Lock()
	_, existsWhileDisabled := engine.tasks[originalID]
	engine.lock.Unlock()
	assert.False(t, existsWhileDisabled, "Disabled task should not be in active tasks map")

	// Verify task is still retrievable from storage
	retrievedTask, err := engine.GetTask(user, originalID)
	assert.NoError(t, err)
	assert.Equal(t, avsproto.TaskStatus_Disabled, retrievedTask.Status)

	// Re-enable
	enableResp, err := engine.SetTaskEnabledByUser(user, originalID, true)
	assert.NoError(t, err)
	assert.True(t, enableResp.Success)
	assert.Equal(t, "enabled", enableResp.Status)

	// Verify task is back in active tasks map
	engine.lock.Lock()
	_, existsAfterReEnable := engine.tasks[originalID]
	engine.lock.Unlock()
	assert.True(t, existsAfterReEnable, "Re-enabled task should be in active tasks map")

	// Verify task data is preserved
	reEnabledTask, err := engine.GetTask(user, originalID)
	assert.NoError(t, err)
	assert.Equal(t, avsproto.TaskStatus_Enabled, reEnabledTask.Status)
	assert.Equal(t, originalID, reEnabledTask.Id)
	assert.NotNil(t, reEnabledTask.Trigger)
	assert.NotNil(t, reEnabledTask.Trigger.GetBlock(), "Trigger config should be preserved after round-trip")
}

// TestReEnableIdempotent verifies that enabling an already-enabled task
// is idempotent and doesn't send duplicate notifications.
func TestReEnableIdempotent(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	err := engine.MustStart()
	assert.NoError(t, err)

	user := testutil.TestUser1()

	// Create a task (starts as Enabled)
	taskReq := testutil.RestTask()
	taskReq.SmartWalletAddress = user.SmartAccountAddress.Hex()
	task, err := engine.CreateTask(user, taskReq)
	assert.NoError(t, err)

	// Set up mock stream
	operatorAddr := "0x997e5d40a32c44a3d93e59fc55c4fd20b7d2d49d"
	mockStream := &MockSyncMessagesServer{}

	engine.streamsMutex.Lock()
	engine.operatorStreams[operatorAddr] = mockStream
	engine.streamsMutex.Unlock()

	engine.lock.Lock()
	engine.trackSyncedTasks[operatorAddr] = &operatorState{
		TaskID: map[string]bool{task.Id: true},
	}
	engine.lock.Unlock()

	// Try to enable an already-enabled task
	resp, err := engine.SetTaskEnabledByUser(user, task.Id, true)
	assert.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, "enabled", resp.Status)
	assert.Equal(t, "Task is already enabled", resp.Message)

	// No notifications should have been sent
	time.Sleep(100 * time.Millisecond)
	messages := mockStream.GetReceivedMessages()
	assert.Len(t, messages, 0, "Idempotent enable should not send any notification")
}

// TestDisableIdempotent verifies that disabling an already-disabled task
// is idempotent and doesn't send duplicate notifications.
func TestDisableIdempotent(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	err := engine.MustStart()
	assert.NoError(t, err)

	user := testutil.TestUser1()

	// Create and disable a task
	taskReq := testutil.RestTask()
	taskReq.SmartWalletAddress = user.SmartAccountAddress.Hex()
	task, err := engine.CreateTask(user, taskReq)
	assert.NoError(t, err)

	_, err = engine.SetTaskEnabledByUser(user, task.Id, false)
	assert.NoError(t, err)

	// Set up mock stream
	operatorAddr := "0x997e5d40a32c44a3d93e59fc55c4fd20b7d2d49d"
	mockStream := &MockSyncMessagesServer{}

	engine.streamsMutex.Lock()
	engine.operatorStreams[operatorAddr] = mockStream
	engine.streamsMutex.Unlock()

	engine.lock.Lock()
	engine.trackSyncedTasks[operatorAddr] = &operatorState{
		TaskID: make(map[string]bool),
	}
	engine.lock.Unlock()

	// Try to disable an already-disabled task
	resp, err := engine.SetTaskEnabledByUser(user, task.Id, false)
	assert.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, "disabled", resp.Status)
	assert.Equal(t, "Task is already disabled", resp.Message)

	// No notifications should have been sent
	engine.sendBatchedNotifications()
	time.Sleep(100 * time.Millisecond)
	messages := mockStream.GetReceivedMessages()
	assert.Len(t, messages, 0, "Idempotent disable should not send any notification")
}

// TestTerminalStatesCannotBeToggled verifies that completed and failed tasks
// cannot be enabled or disabled.
func TestTerminalStatesCannotBeToggled(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	err := engine.MustStart()
	assert.NoError(t, err)

	user := testutil.TestUser1()

	// Create a task and manually set it to Completed status
	taskReq := testutil.RestTask()
	taskReq.SmartWalletAddress = user.SmartAccountAddress.Hex()
	task, err := engine.CreateTask(user, taskReq)
	assert.NoError(t, err)

	// Manually force the task to Completed status in storage
	taskObj, err := engine.GetTask(user, task.Id)
	assert.NoError(t, err)

	oldStatus := taskObj.Status
	taskObj.Task.Status = avsproto.TaskStatus_Completed
	taskJSON, err := taskObj.ToJSON()
	assert.NoError(t, err)

	updates := map[string][]byte{
		string(TaskStorageKey(taskObj.Id, avsproto.TaskStatus_Completed)): taskJSON,
		string(TaskUserKey(taskObj)): []byte(fmt.Sprintf("%d", avsproto.TaskStatus_Completed)),
	}
	err = engine.db.BatchWrite(updates)
	assert.NoError(t, err)
	// Delete old key
	_ = engine.db.Delete(TaskStorageKey(taskObj.Id, oldStatus))

	// Remove from active tasks
	engine.lock.Lock()
	delete(engine.tasks, taskObj.Id)
	engine.lock.Unlock()

	// Try to enable a completed task
	enableResp, err := engine.SetTaskEnabledByUser(user, task.Id, true)
	assert.NoError(t, err)
	assert.False(t, enableResp.Success)
	assert.Equal(t, "error", enableResp.Status)
	assert.Contains(t, enableResp.Message, "terminal status")

	// Try to disable a completed task
	disableResp, err := engine.SetTaskEnabledByUser(user, task.Id, false)
	assert.NoError(t, err)
	assert.False(t, disableResp.Success)
	assert.Equal(t, "error", disableResp.Status)
	assert.Contains(t, disableResp.Message, "terminal status")
}
