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

func TestNotifyOperatorsTaskOperation_CancelTask(t *testing.T) {
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

	// Cancel the task (this should trigger notification)
	cancelled, err := engine.CancelTaskByUser(user, task.Id)
	assert.NoError(t, err)
	assert.True(t, cancelled.Success)

	// Manually trigger batch processing for immediate testing
	engine.sendBatchedNotifications()

	// Give some time for async notification processing
	time.Sleep(100 * time.Millisecond)

	// Verify the notification was sent
	messages := mockStream.GetReceivedMessages()
	messageTypes := mockStream.GetMessageTypes()

	assert.Len(t, messages, 1, "Should have received exactly one notification")
	assert.Equal(t, avsproto.MessageOp_CancelTask, messageTypes[0], "Should have received CancelTask notification")
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
	assert.Equal(t, "Active", resp.PreviousStatus) // Task is active before deletion
}

func TestCancelTaskRespFields(t *testing.T) {
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

	resp, err := engine.CancelTaskByUser(user, task.Id)
	assert.NoError(t, err)
	assert.True(t, resp.Success)
	assert.Equal(t, "cancelled", resp.Status)
	assert.Equal(t, task.Id, resp.Id)
	assert.NotEmpty(t, resp.Message)
	assert.NotZero(t, resp.CancelledAt)
	assert.Equal(t, "Active", resp.PreviousStatus) // Task is active before cancellation
}
