package trigger

import (
	"fmt"
	"sync"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskRegistry_ConvertFromSyncMap(t *testing.T) {
	registry := NewTaskRegistry()

	// Create a legacy sync.Map with test data
	oldChecks := &sync.Map{}

	// Create test task metadata
	taskMetadata := &avsproto.SyncMessagesResp_TaskMetadata{
		TaskId: "test-task-1",
	}

	// Create test queries
	queries := []*avsproto.EventTrigger_Query{
		{
			Addresses: []string{"0x1234567890123456789012345678901234567890"},
			Topics:    []string{"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"},
		},
	}

	// Create legacy Check struct
	legacyCheck := &Check{
		TaskMetadata: taskMetadata,
		Queries:      queries,
		ParsedABIs:   make(map[int]*abi.ABI),
	}

	// Store in old format
	oldChecks.Store("test-task-1", legacyCheck)

	// Test conversion
	registry.ConvertFromSyncMap(oldChecks)

	// Verify conversion worked
	entry, exists := registry.GetTask("test-task-1")
	require.True(t, exists, "Task should exist after conversion")
	require.NotNil(t, entry.EventData, "EventData should be populated")

	assert.Equal(t, "test-task-1", entry.TaskID)
	assert.Equal(t, taskMetadata, entry.TaskMetadata)
	assert.Equal(t, queries, entry.EventData.Queries)
	assert.NotNil(t, entry.EventData.ParsedABIs)
	assert.NotZero(t, entry.CreatedAt)
	assert.NotZero(t, entry.UpdatedAt)
}

func TestTaskRegistry_ConvertFromSyncMap_HandlesBothPointerAndValue(t *testing.T) {
	registry := NewTaskRegistry()
	oldChecks := &sync.Map{}

	// Test with pointer type (*Check)
	taskMetadata1 := &avsproto.SyncMessagesResp_TaskMetadata{TaskId: "task-1"}
	check1 := &Check{
		TaskMetadata: taskMetadata1,
		Queries:      []*avsproto.EventTrigger_Query{},
		ParsedABIs:   make(map[int]*abi.ABI),
	}
	oldChecks.Store("task-1", check1) // Store pointer

	// Test with value type (Check)
	taskMetadata2 := &avsproto.SyncMessagesResp_TaskMetadata{TaskId: "task-2"}
	check2 := Check{
		TaskMetadata: taskMetadata2,
		Queries:      []*avsproto.EventTrigger_Query{},
		ParsedABIs:   make(map[int]*abi.ABI),
	}
	oldChecks.Store("task-2", check2) // Store value

	// Convert
	registry.ConvertFromSyncMap(oldChecks)

	// Both should be converted successfully
	entry1, exists1 := registry.GetTask("task-1")
	assert.True(t, exists1)
	assert.Equal(t, "task-1", entry1.TaskID)

	entry2, exists2 := registry.GetTask("task-2")
	assert.True(t, exists2)
	assert.Equal(t, "task-2", entry2.TaskID)
}

func TestTaskRegistry_ConvertFromSyncMap_SkipsInvalidData(t *testing.T) {
	registry := NewTaskRegistry()
	oldChecks := &sync.Map{}

	// Add invalid key type
	oldChecks.Store(123, &Check{})

	// Add invalid value type
	oldChecks.Store("invalid-value", "not-a-check")

	// Add valid data
	validCheck := &Check{
		TaskMetadata: &avsproto.SyncMessagesResp_TaskMetadata{TaskId: "valid-task"},
		Queries:      []*avsproto.EventTrigger_Query{},
		ParsedABIs:   make(map[int]*abi.ABI),
	}
	oldChecks.Store("valid-task", validCheck)

	// Convert
	registry.ConvertFromSyncMap(oldChecks)

	// Only valid data should be converted
	assert.Equal(t, 1, registry.GetEventTaskCount())

	entry, exists := registry.GetTask("valid-task")
	assert.True(t, exists)
	assert.Equal(t, "valid-task", entry.TaskID)
}

func TestTaskRegistry_BasicOperations(t *testing.T) {
	registry := NewTaskRegistry()

	// Test empty registry
	assert.Equal(t, 0, registry.GetEventTaskCount())
	assert.Equal(t, []string{}, registry.ListTasks())

	// Add a task
	taskMetadata := &avsproto.SyncMessagesResp_TaskMetadata{TaskId: "test-task"}
	eventData := &EventTaskData{
		Queries:    []*avsproto.EventTrigger_Query{},
		ParsedABIs: make(map[int]*abi.ABI),
	}

	registry.AddTask("test-task", taskMetadata, eventData, nil, nil)

	// Verify task was added
	assert.Equal(t, 1, registry.GetEventTaskCount())
	assert.Equal(t, []string{"test-task"}, registry.ListTasks())

	entry, exists := registry.GetTask("test-task")
	assert.True(t, exists)
	assert.Equal(t, "test-task", entry.TaskID)
	assert.Equal(t, taskMetadata, entry.TaskMetadata)
	assert.Equal(t, eventData, entry.EventData)

	// Remove task
	removed := registry.RemoveTask("test-task")
	assert.True(t, removed)
	assert.Equal(t, 0, registry.GetEventTaskCount())
	assert.Equal(t, []string{}, registry.ListTasks())

	// Try to remove non-existent task
	removed = registry.RemoveTask("non-existent")
	assert.False(t, removed)
}

func TestTaskRegistry_RangeEventTasks(t *testing.T) {
	registry := NewTaskRegistry()

	// Add mixed task types
	eventData := &EventTaskData{
		Queries:    []*avsproto.EventTrigger_Query{},
		ParsedABIs: make(map[int]*abi.ABI),
	}
	blockData := &BlockTaskData{Interval: 10}

	registry.AddTask("event-task", &avsproto.SyncMessagesResp_TaskMetadata{TaskId: "event-task"}, eventData, nil, nil)
	registry.AddTask("block-task", &avsproto.SyncMessagesResp_TaskMetadata{TaskId: "block-task"}, nil, blockData, nil)
	registry.AddTask("event-task-2", &avsproto.SyncMessagesResp_TaskMetadata{TaskId: "event-task-2"}, eventData, nil, nil)

	// Range should only return event tasks
	var eventTasks []string
	registry.RangeEventTasks(func(taskID string, entry *TaskEntry) bool {
		eventTasks = append(eventTasks, taskID)
		return true
	})

	assert.Equal(t, 2, len(eventTasks))
	assert.Contains(t, eventTasks, "event-task")
	assert.Contains(t, eventTasks, "event-task-2")
	assert.NotContains(t, eventTasks, "block-task")
}

func TestTaskRegistry_ThreadSafety(t *testing.T) {
	registry := NewTaskRegistry()

	// Test concurrent access
	done := make(chan bool)

	// Writer goroutine
	go func() {
		for i := 0; i < 100; i++ {
			taskID := fmt.Sprintf("task-%d", i)
			eventData := &EventTaskData{
				Queries:    []*avsproto.EventTrigger_Query{},
				ParsedABIs: make(map[int]*abi.ABI),
			}
			registry.AddTask(taskID, &avsproto.SyncMessagesResp_TaskMetadata{TaskId: taskID}, eventData, nil, nil)
		}
		done <- true
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < 100; i++ {
			registry.GetEventTaskCount()
			registry.ListTasks()
			registry.RangeEventTasks(func(taskID string, entry *TaskEntry) bool {
				return true
			})
		}
		done <- true
	}()

	// Wait for both goroutines
	<-done
	<-done

	// Verify final state
	assert.Equal(t, 100, registry.GetEventTaskCount())
}
