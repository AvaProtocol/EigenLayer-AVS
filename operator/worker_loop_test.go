package operator

import (
	"sync"
	"testing"
	"time"

	"github.com/go-co-op/gocron/v2"
)

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

func TestBlockTasksMapCleanup(t *testing.T) {
	blockTasksMap := make(map[int64][]string)
	blockTasksMutex := &sync.Mutex{}
	
	for i := int64(1); i <= 15; i++ {
		blockTasksMap[i] = []string{"task" + string(i)}
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
			blockTasksMap[blockNum] = append(blockTasksMap[blockNum], "task"+string(i))
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
	
	handleBlockTrigger := func(taskID string, blockNum int64) {
		mockLogger.Debug("block trigger details", "task_id", taskID, "marker", blockNum)
		
		blockTasksMap := make(map[int64][]string)
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
		handleBlockTrigger("task"+string(i+2), 100)
	}
	
	if len(mockLogger.debugMessages) != 5 {
		t.Errorf("Expected 5 debug messages, got %d", len(mockLogger.debugMessages))
	}
	
	if len(mockLogger.infoMessages) != 2 {
		t.Errorf("Expected 2 info messages, got %d", len(mockLogger.infoMessages))
	}
}

func TestSchedulerCleanupJob(t *testing.T) {
	scheduler, err := gocron.NewScheduler()
	if err != nil {
		t.Fatalf("Failed to create scheduler: %v", err)
	}
	scheduler.Start()
	defer scheduler.Shutdown()
	
	blockTasksMap := make(map[int64][]string)
	blockTasksMutex := &sync.Mutex{}
	
	for i := int64(1); i <= 15; i++ {
		blockTasksMap[i] = []string{"task" + string(i)}
	}
	
	cleanupDone := make(chan bool)
	
	_, err = scheduler.NewJob(
		gocron.DurationJob(time.Millisecond*100),
		gocron.NewTask(func() {
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
		}),
	)
	if err != nil {
		t.Fatalf("Failed to create cleanup job: %v", err)
	}
	
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
