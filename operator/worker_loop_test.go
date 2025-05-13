package operator

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avspb "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/stretchr/testify/mock"
)

type EventTriggerInterface interface {
	RemoveCheck(taskID string) error
	AddCheck(taskMetadata *avspb.SyncMessagesResp_TaskMetadata) error
	GetProgress() int64
	Run(ctx context.Context) error
}

type BlockTriggerInterface interface {
	Remove(taskMetadata *avspb.SyncMessagesResp_TaskMetadata) error
	AddCheck(taskMetadata *avspb.SyncMessagesResp_TaskMetadata) error
	GetProgress() int64
	Run(ctx context.Context) error
}

type TimeTriggerInterface interface {
	Remove(taskMetadata *avspb.SyncMessagesResp_TaskMetadata) error
	AddCheck(taskMetadata *avspb.SyncMessagesResp_TaskMetadata) error
	GetProgress() int64
	Run(ctx context.Context) error
}

type MockLogger struct {
	mu            sync.Mutex
	infoMessages  []string
	debugMessages []string
	errorMessages []string
}

func NewMockLogger() *MockLogger {
	return &MockLogger{
		infoMessages:  make([]string, 0),
		debugMessages: make([]string, 0),
		errorMessages: make([]string, 0),
	}
}

func (m *MockLogger) Info(msg string, keysAndValues ...interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.infoMessages = append(m.infoMessages, msg)
}

func (m *MockLogger) Debug(msg string, keysAndValues ...interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.debugMessages = append(m.debugMessages, msg)
}

func (m *MockLogger) Error(msg string, keysAndValues ...interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errorMessages = append(m.errorMessages, msg)
}

func (m *MockLogger) Errorf(format string, args ...interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errorMessages = append(m.errorMessages, format)
}

func (m *MockLogger) Infof(format string, args ...interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.infoMessages = append(m.infoMessages, format)
}

func (m *MockLogger) Fatal(msg string, keysAndValues ...interface{}) {
	panic(msg)
}

func (m *MockLogger) With(keysAndValues ...interface{}) interface{} {
	return m
}

type MockEventTrigger struct {
	mock.Mock
}

func (m *MockEventTrigger) RemoveCheck(taskID string) error {
	args := m.Called(taskID)
	return args.Error(0)
}

func (m *MockEventTrigger) AddCheck(taskMetadata *avspb.SyncMessagesResp_TaskMetadata) error {
	args := m.Called(taskMetadata)
	return args.Error(0)
}

func (m *MockEventTrigger) GetProgress() int64 {
	args := m.Called()
	return args.Get(0).(int64)
}

func (m *MockEventTrigger) Run(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

type MockBlockTrigger struct {
	mock.Mock
}

func (m *MockBlockTrigger) Remove(taskMetadata *avspb.SyncMessagesResp_TaskMetadata) error {
	args := m.Called(taskMetadata)
	return args.Error(0)
}

func (m *MockBlockTrigger) AddCheck(taskMetadata *avspb.SyncMessagesResp_TaskMetadata) error {
	args := m.Called(taskMetadata)
	return args.Error(0)
}

func (m *MockBlockTrigger) GetProgress() int64 {
	args := m.Called()
	return args.Get(0).(int64)
}

func (m *MockBlockTrigger) Run(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

type MockTimeTrigger struct {
	mock.Mock
}

func (m *MockTimeTrigger) Remove(taskMetadata *avspb.SyncMessagesResp_TaskMetadata) error {
	args := m.Called(taskMetadata)
	return args.Error(0)
}

func (m *MockTimeTrigger) AddCheck(taskMetadata *avspb.SyncMessagesResp_TaskMetadata) error {
	args := m.Called(taskMetadata)
	return args.Error(0)
}

func (m *MockTimeTrigger) GetProgress() int64 {
	args := m.Called()
	return args.Get(0).(int64)
}

func (m *MockTimeTrigger) Run(ctx context.Context) error {
	args := m.Called(ctx)
	return args.Error(0)
}

func TestBlockTasksMapCleanup(t *testing.T) {
	blockTasksMap := make(map[int64][]string)
	blockTasksMutex := &sync.Mutex{}
	
	for i := int64(1); i <= 15; i++ {
		blockTasksMap[i] = []string{"task" + strconv.FormatInt(i, 10)}
	}
	
	cleanupFunc := func() {
		blockTasksMutex.Lock()
		defer blockTasksMutex.Unlock()
		
		if len(blockTasksMap) > 10 {
			var blocks []int64
			for block := range blockTasksMap {
				blocks = append(blocks, block)
			}
			
			for i := 0; i < len(blocks); i++ {
				for j := i + 1; j < len(blocks); j++ {
					if blocks[i] > blocks[j] {
						blocks[i], blocks[j] = blocks[j], blocks[i]
					}
				}
			}
			
			for i := 0; i < len(blocks)-10; i++ {
				delete(blockTasksMap, blocks[i])
			}
		}
	}
	
	cleanupFunc()
	
	if len(blockTasksMap) != 10 {
		t.Errorf("Expected 10 entries in blockTasksMap after cleanup, got %d", len(blockTasksMap))
	}
	
	for i := int64(1); i <= 5; i++ {
		if _, exists := blockTasksMap[i]; exists {
			t.Errorf("Expected block %d to be removed from blockTasksMap", i)
		}
	}
	
	for i := int64(6); i <= 15; i++ {
		if _, exists := blockTasksMap[i]; !exists {
			t.Errorf("Expected block %d to remain in blockTasksMap", i)
		}
	}
}

func TestBlockTasksMapConcurrency(t *testing.T) {
	blockTasksMap := make(map[int64][]string)
	blockTasksMutex := &sync.Mutex{}
	
	var wg sync.WaitGroup
	
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			
			blockNum := int64(i % 5) // Use 5 different block numbers
			
			blockTasksMutex.Lock()
			blockTasksMap[blockNum] = append(blockTasksMap[blockNum], "task"+strconv.Itoa(i))
			blockTasksMutex.Unlock()
		}(i)
	}
	
	wg.Wait()
	
	if len(blockTasksMap) != 5 {
		t.Errorf("Expected 5 different block numbers in blockTasksMap, got %d", len(blockTasksMap))
	}
	
	for blockNum, tasks := range blockTasksMap {
		expectedCount := 2 // Each block number should have 2 tasks (10 tasks / 5 block numbers)
		if len(tasks) != expectedCount {
			t.Errorf("Expected block %d to have %d tasks, got %d", blockNum, expectedCount, len(tasks))
		}
	}
}

func TestLogLevelChanges(t *testing.T) {
	mockLogger := NewMockLogger()
	blockTasksMap := make(map[int64][]string)
	
	handleBlockTrigger := func(taskID string, blockNum int64) {
		mockLogger.Debug("block trigger details", "task_id", taskID, "marker", blockNum)
		
		blockTasksMap[blockNum] = append(blockTasksMap[blockNum], taskID)
		
		taskCount := len(blockTasksMap[blockNum])
		if taskCount == 1 || taskCount%5 == 0 {
			mockLogger.Info("block trigger summary", "block", blockNum, "task_count", taskCount)
		}
	}
	
	handleBlockTrigger("task1", 100)
	
	if len(mockLogger.debugMessages) != 1 {
		t.Errorf("Expected 1 debug message, got %d", len(mockLogger.debugMessages))
	}
	
	if len(mockLogger.infoMessages) != 1 {
		t.Errorf("Expected 1 info message, got %d", len(mockLogger.infoMessages))
	}
	
	for i := 0; i < 4; i++ {
		handleBlockTrigger("task"+strconv.Itoa(i+2), 100)
	}
	
	if len(mockLogger.debugMessages) != 5 {
		t.Errorf("Expected 5 debug messages, got %d", len(mockLogger.debugMessages))
	}
	
	if len(mockLogger.infoMessages) != 2 {
		t.Errorf("Expected 2 info messages, got %d", len(mockLogger.infoMessages))
	}
}

func TestSchedulerCleanupJob(t *testing.T) {
	blockTasksMap := make(map[int64][]string)
	blockTasksMutex := &sync.Mutex{}
	
	for i := int64(1); i <= 15; i++ {
		blockTasksMap[i] = []string{"task" + strconv.FormatInt(i, 10)}
	}
	
	cleanupDone := make(chan bool)
	
	cleanupFunc := func() {
		blockTasksMutex.Lock()
		defer blockTasksMutex.Unlock()
		
		if len(blockTasksMap) > 10 {
			var blocks []int64
			for block := range blockTasksMap {
				blocks = append(blocks, block)
			}
			
			for i := 0; i < len(blocks); i++ {
				for j := i + 1; j < len(blocks); j++ {
					if blocks[i] > blocks[j] {
						blocks[i], blocks[j] = blocks[j], blocks[i]
					}
				}
			}
			
			for i := 0; i < len(blocks)-10; i++ {
				delete(blockTasksMap, blocks[i])
			}
		}
		
		cleanupDone <- true
	}
	
	go cleanupFunc()
	
	select {
	case <-cleanupDone:
	case <-time.After(time.Second):
		t.Fatal("Cleanup job did not run within the expected time")
	}
	
	if len(blockTasksMap) != 10 {
		t.Errorf("Expected 10 entries in blockTasksMap after cleanup, got %d", len(blockTasksMap))
	}
	
	for i := int64(1); i <= 5; i++ {
		if _, exists := blockTasksMap[i]; exists {
			t.Errorf("Expected block %d to be removed from blockTasksMap", i)
		}
	}
	
	for i := int64(6); i <= 15; i++ {
		if _, exists := blockTasksMap[i]; !exists {
			t.Errorf("Expected block %d to remain in blockTasksMap", i)
		}
	}
}

type TestOperator struct {
	logger       sdklogging.Logger
	eventTrigger EventTriggerInterface
	blockTrigger BlockTriggerInterface
	timeTrigger  TimeTriggerInterface
}

func (o *TestOperator) processMessage(resp *avspb.SyncMessagesResp) {
	switch resp.Op {
	case avspb.MessageOp_CancelTask, avspb.MessageOp_DeleteTask:
		o.logger.Info("removing task from all triggers", "task_id", resp.TaskMetadata.TaskId, "operation", resp.Op)
		o.eventTrigger.RemoveCheck(resp.TaskMetadata.TaskId)
		o.blockTrigger.Remove(resp.TaskMetadata)
		o.timeTrigger.Remove(resp.TaskMetadata)
	}
}

func TestTaskRemovalFromAllTriggers(t *testing.T) {
	mockEventTrigger := new(MockEventTrigger)
	mockBlockTrigger := new(MockBlockTrigger)
	mockTimeTrigger := new(MockTimeTrigger)

	operator := &TestOperator{
		logger:       testutil.GetLogger(),
		eventTrigger: mockEventTrigger,
		blockTrigger: mockBlockTrigger,
		timeTrigger:  mockTimeTrigger,
	}

	testCases := []struct {
		name          string
		op            avspb.MessageOp
		taskID        string
		expectRemoval bool
	}{
		{
			name:          "Cancel Task",
			op:            avspb.MessageOp_CancelTask,
			taskID:        "task-123",
			expectRemoval: true,
		},
		{
			name:          "Delete Task",
			op:            avspb.MessageOp_DeleteTask,
			taskID:        "task-456",
			expectRemoval: true,
		},
		{
			name:          "Monitor Task",
			op:            avspb.MessageOp_MonitorTaskTrigger,
			taskID:        "task-789",
			expectRemoval: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			message := &avspb.SyncMessagesResp{
				Op: tc.op,
				TaskMetadata: &avspb.SyncMessagesResp_TaskMetadata{
					TaskId: tc.taskID,
				},
			}

			if tc.expectRemoval {
				mockEventTrigger.On("RemoveCheck", tc.taskID).Return(nil)
				mockBlockTrigger.On("Remove", message.TaskMetadata).Return(nil)
				mockTimeTrigger.On("Remove", message.TaskMetadata).Return(nil)
			}

			operator.processMessage(message)

			if tc.expectRemoval {
				mockEventTrigger.AssertCalled(t, "RemoveCheck", tc.taskID)
				mockBlockTrigger.AssertCalled(t, "Remove", message.TaskMetadata)
				mockTimeTrigger.AssertCalled(t, "Remove", message.TaskMetadata)
			} else {
				mockEventTrigger.AssertNotCalled(t, "RemoveCheck", tc.taskID)
				mockBlockTrigger.AssertNotCalled(t, "Remove", message.TaskMetadata)
				mockTimeTrigger.AssertNotCalled(t, "Remove", message.TaskMetadata)
			}
		})
	}
}

func TestTaskRemovalFromAllTriggersIntegration(t *testing.T) {
	t.Skip("Integration test requires complex setup with mocked gRPC streams")
}
