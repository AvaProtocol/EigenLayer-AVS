package migrations

import (
	"fmt"
	"log"
	"sort"
	"strings"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/encoding/protojson"
)

// ExecutionWithKey holds an execution along with its storage key for sorting
type ExecutionWithKey struct {
	Key       string
	Execution *avsproto.Execution
	TaskID    string
	ULID      ulid.ULID
}

// AddExecutionIndexes adds proper execution indexes to all existing executions
// This migration sorts executions by their ULID creation time and assigns sequential indexes
func AddExecutionIndexes(db storage.Storage) (int, error) {
	log.Printf("Starting migration: Add execution indexes to existing executions")

	// Get all execution keys from the database
	executionKeys, err := db.ListKeys("history:*")
	if err != nil {
		return 0, fmt.Errorf("failed to list execution keys: %w", err)
	}

	if len(executionKeys) == 0 {
		log.Printf("No existing executions found, migration completed successfully")
		return 0, nil
	}

	log.Printf("Found %d execution records to process", len(executionKeys))

	// Group executions by task ID
	taskExecutions := make(map[string][]ExecutionWithKey)

	for _, key := range executionKeys {
		keyStr := string(key)
		// Key format: "history:<taskID>:<executionID>"
		parts := strings.Split(keyStr, ":")
		if len(parts) != 3 || parts[0] != "history" {
			log.Printf("Skipping malformed key: %s", keyStr)
			continue
		}

		taskID := parts[1]
		executionIDStr := parts[2]

		// Parse the ULID for sorting
		executionULID, err := ulid.Parse(executionIDStr)
		if err != nil {
			log.Printf("Warning: Could not parse ULID for execution %s, skipping: %v", executionIDStr, err)
			continue
		}

		// Load the execution data
		executionData, err := db.GetKey([]byte(keyStr))
		if err != nil {
			log.Printf("Warning: Could not load execution data for key %s: %v", keyStr, err)
			continue
		}

		// Parse the execution
		var execution avsproto.Execution
		if err := protojson.Unmarshal(executionData, &execution); err != nil {
			log.Printf("Warning: Could not unmarshal execution for key %s: %v", keyStr, err)
			continue
		}

		// Create execution with key wrapper
		execWithKey := ExecutionWithKey{
			Key:       keyStr,
			Execution: &execution,
			TaskID:    taskID,
			ULID:      executionULID,
		}

		// Group by task ID
		taskExecutions[taskID] = append(taskExecutions[taskID], execWithKey)
	}

	recordsUpdated := 0

	// Process each task's executions
	for taskID, executions := range taskExecutions {
		log.Printf("Processing %d executions for task %s", len(executions), taskID)

		// Sort executions by ULID (which sorts by creation time)
		sort.Slice(executions, func(i, j int) bool {
			return executions[i].ULID.Compare(executions[j].ULID) < 0
		})

		// Assign sequential indexes starting from 0
		for index, execWithKey := range executions {
			execWithKey.Execution.Index = int64(index)

			// Serialize the updated execution
			updatedData, err := protojson.Marshal(execWithKey.Execution)
			if err != nil {
				log.Printf("Error: Failed to marshal updated execution for key %s: %v", execWithKey.Key, err)
				continue
			}

			// Store the updated execution back to the database
			if err := db.Set([]byte(execWithKey.Key), updatedData); err != nil {
				log.Printf("Error: Failed to store updated execution for key %s: %v", execWithKey.Key, err)
				continue
			}

			recordsUpdated++
		}

		log.Printf("Updated %d executions for task %s with indexes 0-%d",
			len(executions), taskID, len(executions)-1)
	}

	log.Printf("Migration completed successfully. Updated %d execution records with proper indexes.", recordsUpdated)
	return recordsUpdated, nil
}
