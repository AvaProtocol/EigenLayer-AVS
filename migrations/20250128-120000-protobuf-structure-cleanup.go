package migrations

import (
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"google.golang.org/protobuf/types/known/structpb"
)

// ProtobufStructureCleanupMigration handles cleanup of incompatible data structures
// after major protobuf changes in v1.9.6. This migration focuses on removing/canceling
// workflows that use old data structures rather than attempting backward compatibility.
func ProtobufStructureCleanupMigration(db storage.Storage) (int, error) {
	log.Printf("Starting migration: Protobuf structure cleanup migration for v1.9.6")

	recordsUpdated := 0
	workflowsCanceled := 0
	executionsDeleted := 0

	// 1. Clean up workflows with incompatible trigger structures
	workflowKeys, err := db.ListKeys("task:")
	if err != nil {
		return 0, fmt.Errorf("failed to list workflow keys: %w", err)
	}

	log.Printf("Found %d workflows to examine", len(workflowKeys))

	for _, key := range workflowKeys {
		data, err := db.GetKey([]byte(key))
		if err != nil {
			log.Printf("Warning: Failed to get workflow %s: %v", key, err)
			continue
		}

		// Create a model.Task and load from storage data
		task := model.NewTask()
		if err := task.FromStorageData(data); err != nil {
			log.Printf("Warning: Failed to unmarshal workflow %s: %v", key, err)
			continue
		}

		shouldCancel := false
		reasons := []string{}

		// Check for incompatible trigger structures
		if task.Task.Trigger != nil {
			// Check for old manual trigger structure (boolean instead of ManualTrigger)
			if task.Task.Trigger.GetManual() != nil {
				// In the new structure, manual trigger should be a proper ManualTrigger message
				// If it's just a boolean (old structure), we need to cancel
				if manualTrigger := task.Task.Trigger.GetManual(); manualTrigger != nil {
					// Check if it has the new structure with Config
					if manualTrigger.Config == nil {
						shouldCancel = true
						reasons = append(reasons, "old manual trigger boolean structure")
					}
				}
			}
		}

		// Check for incompatible node structures
		for _, node := range task.Task.Nodes {
			// Check for loop/filter nodes with old source_id field
			if loopNode := node.GetLoop(); loopNode != nil {
				if loopNode.Config != nil && loopNode.Config.InputNodeName == "" {
					// If InputNodeName is empty, this might be using the old SourceId field
					shouldCancel = true
					reasons = append(reasons, fmt.Sprintf("loop node %s may use deprecated source_id field", node.Name))
				}
			}

			if filterNode := node.GetFilter(); filterNode != nil {
				if filterNode.Config != nil && filterNode.Config.InputNodeName == "" {
					// If InputNodeName is empty, this might be using the old SourceId field
					shouldCancel = true
					reasons = append(reasons, fmt.Sprintf("filter node %s may use deprecated source_id field", node.Name))
				}
			}
		}

		if shouldCancel {
			log.Printf("Canceling workflow %s due to incompatible structure: %s",
				task.Task.Id, strings.Join(reasons, ", "))

			// Mark workflow as canceled
			task.Task.Status = avsproto.TaskStatus_Canceled
			now := time.Now().Unix()
			task.Task.CompletedAt = now

			// Update task name to indicate migration cancellation
			if task.Task.Name == "" {
				task.Task.Name = fmt.Sprintf("Auto-canceled during v1.9.6 migration: %s", strings.Join(reasons, ", "))
			} else {
				task.Task.Name += fmt.Sprintf(" [Migration: %s]", strings.Join(reasons, ", "))
			}

			// Save the canceled workflow
			updatedData, err := task.ToJSON()
			if err != nil {
				log.Printf("Warning: Failed to marshal canceled workflow %s: %v", key, err)
				continue
			}

			if err := db.Set([]byte(key), updatedData); err != nil {
				log.Printf("Warning: Failed to save canceled workflow %s: %v", key, err)
				continue
			}

			workflowsCanceled++
			recordsUpdated++
		}
	}

	// 2. Clean up execution data with old trigger output structures
	executionKeys, err := db.ListKeys("execution:")
	if err != nil {
		log.Printf("Warning: Failed to list execution keys: %v", err)
	} else {
		log.Printf("Found %d executions to examine", len(executionKeys))

		for _, key := range executionKeys {
			data, err := db.GetKey([]byte(key))
			if err != nil {
				log.Printf("Warning: Failed to get execution %s: %v", key, err)
				continue
			}

			// Try to parse as JSON to detect old structure
			var executionData map[string]interface{}
			if err := json.Unmarshal(data, &executionData); err != nil {
				log.Printf("Warning: Failed to unmarshal execution %s: %v", key, err)
				continue
			}

			shouldDelete := false

			// Check for old trigger output structures in steps
			if steps, ok := executionData["steps"].([]interface{}); ok {
				for _, step := range steps {
					if stepMap, ok := step.(map[string]interface{}); ok {
						// Check for old trigger output structures
						if stepMap["block_trigger"] != nil {
							if blockTrigger, ok := stepMap["block_trigger"].(map[string]interface{}); ok {
								// Old structure has block_number, block_hash, etc. instead of data field
								if _, hasBlockNumber := blockTrigger["block_number"]; hasBlockNumber {
									if _, hasData := blockTrigger["data"]; !hasData {
										shouldDelete = true
										break
									}
								}
							}
						}

						// Check for old manual trigger structure
						if stepMap["manual_trigger"] != nil {
							if manualTrigger, ok := stepMap["manual_trigger"].(map[string]interface{}); ok {
								// Old structure has run_at instead of data field
								if _, hasRunAt := manualTrigger["run_at"]; hasRunAt {
									if _, hasData := manualTrigger["data"]; !hasData {
										shouldDelete = true
										break
									}
								}
							}
						}

						// Check for deprecated input field in steps
						if _, hasInput := stepMap["input"]; hasInput {
							if _, hasConfig := stepMap["config"]; !hasConfig {
								shouldDelete = true
								break
							}
						}
					}
				}
			}

			if shouldDelete {
				log.Printf("Deleting execution %s with incompatible structure", key)
				if err := db.Delete([]byte(key)); err != nil {
					log.Printf("Warning: Failed to delete execution %s: %v", key, err)
					continue
				}
				executionsDeleted++
				recordsUpdated++
			}
		}
	}

	// 3. Clean up any cached trigger data with old structures
	triggerKeys, err := db.ListKeys("trigger:")
	if err != nil {
		log.Printf("Warning: Failed to list trigger keys: %v", err)
	} else {
		for _, key := range triggerKeys {
			data, err := db.GetKey([]byte(key))
			if err != nil {
				continue
			}

			// Check if this is old trigger data by trying to detect old structure
			var triggerData map[string]interface{}
			if err := json.Unmarshal(data, &triggerData); err != nil {
				continue
			}

			// Delete any cached trigger data - it will be regenerated with new structure
			log.Printf("Cleaning cached trigger data: %s", key)
			if err := db.Delete([]byte(key)); err != nil {
				log.Printf("Warning: Failed to delete cached trigger data %s: %v", key, err)
				continue
			}
			recordsUpdated++
		}
	}

	// 4. Clean up any node execution cache with old structures
	nodeKeys, err := db.ListKeys("node:")
	if err != nil {
		log.Printf("Warning: Failed to list node keys: %v", err)
	} else {
		for _, key := range nodeKeys {
			// Delete any cached node data - it will be regenerated with new structure
			log.Printf("Cleaning cached node data: %s", key)
			if err := db.Delete([]byte(key)); err != nil {
				log.Printf("Warning: Failed to delete cached node data %s: %v", key, err)
				continue
			}
			recordsUpdated++
		}
	}

	log.Printf("Migration completed successfully.")
	log.Printf("- Total records updated: %d", recordsUpdated)
	log.Printf("- Workflows canceled: %d", workflowsCanceled)
	log.Printf("- Executions deleted: %d", executionsDeleted)
	log.Printf("- Cached data cleaned: %d", recordsUpdated-workflowsCanceled-executionsDeleted)

	return recordsUpdated, nil
}

// Helper function to check if a protobuf Value contains old structure
func hasOldProtobufStructure(value *structpb.Value) bool {
	if value == nil {
		return false
	}

	// Convert to map for easier inspection
	if structVal := value.GetStructValue(); structVal != nil {
		fields := structVal.GetFields()

		// Check for old trigger output field patterns
		if _, hasBlockNumber := fields["block_number"]; hasBlockNumber {
			if _, hasData := fields["data"]; !hasData {
				return true
			}
		}

		if _, hasRunAt := fields["run_at"]; hasRunAt {
			if _, hasData := fields["data"]; !hasData {
				return true
			}
		}

		if _, hasTimestamp := fields["timestamp"]; hasTimestamp {
			if _, hasData := fields["data"]; !hasData {
				return true
			}
		}
	}

	return false
}
