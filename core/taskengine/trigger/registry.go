package trigger

import (
	"sync"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/accounts/abi"
	gocron "github.com/go-co-op/gocron/v2"
)

// TaskRegistry provides a unified, type-safe way to manage tasks across all trigger types
type TaskRegistry struct {
	mu    sync.RWMutex
	tasks map[string]*TaskEntry
}

// TaskEntry represents a single task with trigger-specific data
type TaskEntry struct {
	TaskID       string
	TaskMetadata *avsproto.SyncMessagesResp_TaskMetadata

	// Trigger-specific data (only one will be populated based on trigger type)
	EventData *EventTaskData
	BlockData *BlockTaskData
	TimeData  *TimeTaskData

	CreatedAt time.Time
	UpdatedAt time.Time
}

// EventTaskData contains event trigger specific information
type EventTaskData struct {
	Queries         []*avsproto.EventTrigger_Query
	ParsedABIs      map[int]*abi.ABI // queryIndex -> parsed ABI
	CooldownSeconds uint32           // Cooldown period in seconds (default: DefaultEventTriggerCooldownSeconds)
}

// BlockTaskData contains block trigger specific information
type BlockTaskData struct {
	Interval int64
}

// TimeTaskData contains time trigger specific data
type TimeTaskData struct {
	Jobs      []gocron.Job // All scheduled jobs (one per cron schedule + epochs)
	Schedules []string     // Original cron schedules
	Epochs    []int64      // Original epochs for fixed time triggers
}

// NewTaskRegistry creates a new unified task registry
func NewTaskRegistry() *TaskRegistry {
	return &TaskRegistry{
		tasks: make(map[string]*TaskEntry),
	}
}

// AddTask adds a new task to the registry
func (r *TaskRegistry) AddTask(taskID string, taskMetadata *avsproto.SyncMessagesResp_TaskMetadata, eventData *EventTaskData, blockData *BlockTaskData, timeData *TimeTaskData) {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	entry := &TaskEntry{
		TaskID:       taskID,
		TaskMetadata: taskMetadata,
		EventData:    eventData,
		BlockData:    blockData,
		TimeData:     timeData,
		CreatedAt:    now,
		UpdatedAt:    now,
	}

	r.tasks[taskID] = entry
}

// GetTask retrieves a task by ID
func (r *TaskRegistry) GetTask(taskID string) (*TaskEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, exists := r.tasks[taskID]
	return entry, exists
}

// RemoveTask removes a task from the registry
func (r *TaskRegistry) RemoveTask(taskID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	if entry, exists := r.tasks[taskID]; exists {
		// Clean up EventData if present
		if entry.EventData != nil {
			// Clear cached ABIs to free memory
			for queryIndex := range entry.EventData.ParsedABIs {
				delete(entry.EventData.ParsedABIs, queryIndex)
			}
		}

		delete(r.tasks, taskID)
		return true
	}
	return false
}

// ListTasks returns all task IDs
func (r *TaskRegistry) ListTasks() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	taskIDs := make([]string, 0, len(r.tasks))
	for taskID := range r.tasks {
		taskIDs = append(taskIDs, taskID)
	}
	return taskIDs
}

// RangeEventTasks iterates over all tasks with EventData
func (r *TaskRegistry) RangeEventTasks(fn func(taskID string, entry *TaskEntry) bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for taskID, entry := range r.tasks {
		if entry.EventData != nil {
			if !fn(taskID, entry) {
				break
			}
		}
	}
}

// GetEventTaskCount returns the number of tasks with EventData
func (r *TaskRegistry) GetEventTaskCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, entry := range r.tasks {
		if entry.EventData != nil {
			count++
		}
	}
	return count
}

// GetBlockTaskCount returns the number of tasks with BlockData
func (r *TaskRegistry) GetBlockTaskCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, entry := range r.tasks {
		if entry.BlockData != nil {
			count++
		}
	}
	return count
}

// RangeBlockTasks iterates over all tasks with BlockData
func (r *TaskRegistry) RangeBlockTasks(fn func(taskID string, entry *TaskEntry) bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for taskID, entry := range r.tasks {
		if entry.BlockData != nil {
			if !fn(taskID, entry) {
				break
			}
		}
	}
}

// GetTimeTaskCount returns the number of tasks with TimeData
func (r *TaskRegistry) GetTimeTaskCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, entry := range r.tasks {
		if entry.TimeData != nil {
			count++
		}
	}
	return count
}

// RangeTimeTasks iterates over all tasks with TimeData
func (r *TaskRegistry) RangeTimeTasks(fn func(taskID string, entry *TaskEntry) bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for taskID, entry := range r.tasks {
		if entry.TimeData != nil {
			if !fn(taskID, entry) {
				break
			}
		}
	}
}

// Legacy conversion functions for backward compatibility

// ConvertFromSyncMap converts old sync.Map data to the new TaskRegistry
// This provides backward compatibility during migration
func (r *TaskRegistry) ConvertFromSyncMap(oldChecks *sync.Map) {
	r.mu.Lock()
	defer r.mu.Unlock()

	oldChecks.Range(func(key, value interface{}) bool {
		taskID, ok := key.(string)
		if !ok {
			return true // Skip invalid keys
		}

		// Handle both *Check and Check types for maximum compatibility
		var check *Check
		switch v := value.(type) {
		case *Check:
			check = v
		case Check:
			check = &v
		default:
			return true // Skip invalid values
		}

		// Convert to new format
		// Legacy format doesn't have cooldown_seconds, so use default value
		eventData := &EventTaskData{
			Queries:         check.Queries,
			ParsedABIs:      check.ParsedABIs,
			CooldownSeconds: DefaultEventTriggerCooldownSeconds, // Legacy format uses default cooldown
		}

		now := time.Now()
		entry := &TaskEntry{
			TaskID:       taskID,
			TaskMetadata: check.TaskMetadata,
			EventData:    eventData,
			CreatedAt:    now,
			UpdatedAt:    now,
		}

		r.tasks[taskID] = entry
		return true
	})
}

// Check if we need to convert from old format
func (r *TaskRegistry) NeedsConversion() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tasks) == 0
}
