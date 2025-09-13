//go:build migrations

// The build tags require `go build -tags="migrations"` specifically to include this file in the build
// Since this migration has run we do not want to include this file in a normal build

package migrations

import (
	"fmt"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/protobuf/encoding/protojson" // Correct import for protojson
)

func TestChangeEpochToMs(t *testing.T) {
	// --- Setup ---
	logger := testutil.GetLogger() // Use logger from testutil if needed by setup funcs
	_ = logger                     // Avoid unused variable error if logger isn't directly used
	db := testutil.TestMustDB()
	defer db.Close()

	// Sample data IDs
	taskID := "task-123"
	execID := "exec-abc"

	// Sample timestamps in seconds (ensure they are below the migration threshold)
	nowSeconds := time.Now().Unix() // e.g., 1715000000
	startSeconds := nowSeconds - 3600
	expiredSeconds := nowSeconds + 86400
	completedSeconds := nowSeconds - 60
	lastRanSeconds := nowSeconds - 120
	execStartSeconds := nowSeconds - 30
	execEndSeconds := nowSeconds - 5
	stepStartSeconds := execStartSeconds + 1
	stepEndSeconds := execEndSeconds - 1
	blockTimestampSeconds := nowSeconds - 10
	epochSeconds := nowSeconds - 15

	// Expected timestamps in milliseconds
	expectedStartMs := startSeconds * 1000
	expectedExpiredMs := expiredSeconds * 1000
	expectedCompletedMs := completedSeconds * 1000
	expectedLastRanMs := lastRanSeconds * 1000
	expectedExecStartMs := execStartSeconds * 1000
	expectedExecEndMs := execEndSeconds * 1000
	expectedStepStartMs := stepStartSeconds * 1000
	expectedStepEndMs := stepEndSeconds * 1000
	expectedBlockTimestampMs := blockTimestampSeconds * 1000
	expectedEpochMs := epochSeconds * 1000

	// Create Sample Task Data (using avsproto.Task directly as migration handles it)
	sampleTask := &avsproto.Task{
		Id:          taskID,
		StartAt:     startSeconds,     // Seconds
		ExpiredAt:   expiredSeconds,   // Seconds
		CompletedAt: completedSeconds, // Seconds
		LastRanAt:   lastRanSeconds,   // Seconds
		// Other fields can be default/empty for this test
	}

	// Create Sample Execution Data
	sampleExec := &avsproto.Execution{
		Id:             execID,
		StartAt:        execStartSeconds, // Seconds
		EndAt:          execEndSeconds,   // Seconds
		Index:          0,                // Migration test execution
		Steps: []*avsproto.Execution_Step{
			{
				Id:      "step-1",
				Success: true,
				StartAt: stepStartSeconds, // Seconds
				EndAt:   stepEndSeconds,   // Seconds
				OutputData: &avsproto.Execution_Step_EventTrigger{
					EventTrigger: &avsproto.EventTrigger_Output{
						Data: fmt.Sprintf(`{"blockTimestamp": %d}`, blockTimestampSeconds*1000), // Convert to ms in JSON
					},
				},
			},
		},
		// Other fields can be default/empty
	}
	// Add another execution to test TimeOutput
	execID2 := "exec-def"
	sampleExec2 := &avsproto.Execution{
		Id:             execID2,
		StartAt:        execStartSeconds, // Seconds
		EndAt:          execEndSeconds,   // Seconds
		Index:          1,                // Migration test execution (second)
		Steps: []*avsproto.Execution_Step{
			{
				Id:      "step-2",
				Success: true,
				StartAt: stepStartSeconds, // Seconds
				EndAt:   stepEndSeconds,   // Seconds
				OutputData: &avsproto.Execution_Step_FixedTimeTrigger{
					FixedTimeTrigger: &avsproto.FixedTimeTrigger_Output{
						Timestamp: uint64(epochSeconds), // Seconds (will be converted to ms by migration)
					},
				},
			},
		},
		// Other fields can be default/empty
	}

	// Serialize data using protojson (matching migration)
	var err error
	var execData, execData2 []byte
	var taskBytes []byte

	taskBytes, err = protojson.Marshal(sampleTask)
	if err != nil {
		t.Fatalf("Failed to marshal sample task: %v", err)
	}
	execData, err = protojson.Marshal(sampleExec)
	if err != nil {
		t.Fatalf("Failed to marshal sample execution: %v", err)
	}
	execData2, err = protojson.Marshal(sampleExec2)
	if err != nil {
		t.Fatalf("Failed to marshal sample execution 2: %v", err)
	}

	// Store data in DB using correct keys
	taskKey := fmt.Sprintf("t:%s", taskID)
	execKey := fmt.Sprintf("history:%s:%s", taskID, execID)
	execKey2 := fmt.Sprintf("history:%s:%s", taskID, execID2)

	// Use BatchWrite as seen in the migration code for setting multiple keys
	updates := map[string][]byte{
		taskKey:  taskBytes,
		execKey:  execData,
		execKey2: execData2,
	}
	if err := db.BatchWrite(updates); err != nil {
		t.Fatalf("Failed to write initial data to db: %v", err)
	}

	// --- Execute Migration ---
	updatedCount, err := ChangeEpochToMs(db)
	if err != nil {
		t.Fatalf("Migration function failed: %v", err)
	}
	// Check if the expected number of records were reported as updated
	// Modify '3' if you add/remove test records
	expectedUpdates := 3
	if updatedCount != expectedUpdates {
		t.Errorf("Migration reported updating %d records, expected %d", updatedCount, expectedUpdates)
	}

	// --- Verification ---

	// Verify Task Data using GetKey
	retrievedTaskBytes, err := db.GetKey([]byte(taskKey))
	if err != nil {
		t.Fatalf("Failed to retrieve task data after migration: %v", err)
	}
	retrievedTask := &avsproto.Task{}
	if err := protojson.Unmarshal(retrievedTaskBytes, retrievedTask); err != nil {
		t.Fatalf("Failed to unmarshal retrieved task data: %v", err)
	}

	if retrievedTask.StartAt != expectedStartMs {
		t.Errorf("Task StartAt incorrect: got %d, want %d", retrievedTask.StartAt, expectedStartMs)
	}
	if retrievedTask.ExpiredAt != expectedExpiredMs {
		t.Errorf("Task ExpiredAt incorrect: got %d, want %d", retrievedTask.ExpiredAt, expectedExpiredMs)
	}
	if retrievedTask.CompletedAt != expectedCompletedMs {
		t.Errorf("Task CompletedAt incorrect: got %d, want %d", retrievedTask.CompletedAt, expectedCompletedMs)
	}
	if retrievedTask.LastRanAt != expectedLastRanMs {
		t.Errorf("Task LastRanAt incorrect: got %d, want %d", retrievedTask.LastRanAt, expectedLastRanMs)
	}

	// Verify Execution Data 1 (TransferLog) using GetKey
	retrievedExecBytes, err := db.GetKey([]byte(execKey))
	if err != nil {
		t.Fatalf("Failed to retrieve execution data after migration: %v", err)
	}
	retrievedExec := &avsproto.Execution{}
	if err := protojson.Unmarshal(retrievedExecBytes, retrievedExec); err != nil {
		t.Fatalf("Failed to unmarshal retrieved execution data: %v", err)
	}

	if retrievedExec.StartAt != expectedExecStartMs {
		t.Errorf("Execution StartAt incorrect: got %d, want %d", retrievedExec.StartAt, expectedExecStartMs)
	}
	if retrievedExec.EndAt != expectedExecEndMs {
		t.Errorf("Execution EndAt incorrect: got %d, want %d", retrievedExec.EndAt, expectedExecEndMs)
	}

	if len(retrievedExec.Steps) != 1 {
		t.Fatalf("Incorrect number of steps retrieved: got %d, want 1", len(retrievedExec.Steps))
	}
	retrievedStep := retrievedExec.Steps[0]
	if retrievedStep.StartAt != expectedStepStartMs {
		t.Errorf("Step StartAt incorrect: got %d, want %d", retrievedStep.StartAt, expectedStepStartMs)
	}
	if retrievedStep.EndAt != expectedStepEndMs {
		t.Errorf("Step EndAt incorrect: got %d, want %d", retrievedStep.EndAt, expectedStepEndMs)
	}

	// Verify Execution Step Output Data (EventTrigger with JSON data)
	if len(retrievedExec.Steps) > 0 {
		step := retrievedExec.Steps[0]
		if eventTriggerOutput := step.GetEventTrigger(); eventTriggerOutput != nil {
			// Parse the JSON data to verify blockTimestamp was converted
			expectedJSON := fmt.Sprintf(`{"blockTimestamp": %d}`, expectedBlockTimestampMs)
			if eventTriggerOutput.Data != expectedJSON {
				t.Errorf("EventTrigger JSON data incorrect: got %s, want %s", eventTriggerOutput.Data, expectedJSON)
			}
		} else {
			t.Errorf("Expected EventTrigger output data in step, but got nil or different type")
		}
	} else {
		t.Errorf("Expected execution to have steps with EventTrigger output")
	}

	// Verify Execution Data 2 (TimeOutput) using GetKey
	retrievedExecBytes2, err := db.GetKey([]byte(execKey2))
	if err != nil {
		t.Fatalf("Failed to retrieve execution data 2 after migration: %v", err)
	}
	retrievedExec2 := &avsproto.Execution{}
	if err := protojson.Unmarshal(retrievedExecBytes2, retrievedExec2); err != nil {
		t.Fatalf("Failed to unmarshal retrieved execution data 2: %v", err)
	}

	// Verify Execution Step Output Data (FixedTimeTrigger)
	if len(retrievedExec2.Steps) > 0 {
		step2 := retrievedExec2.Steps[0]
		if fixedTimeTriggerOutput := step2.GetFixedTimeTrigger(); fixedTimeTriggerOutput != nil {
			if fixedTimeTriggerOutput.Timestamp != uint64(expectedEpochMs) {
				t.Errorf("FixedTimeTrigger Timestamp incorrect: got %d, want %d", fixedTimeTriggerOutput.Timestamp, expectedEpochMs)
			}
		} else {
			t.Errorf("Expected FixedTimeTrigger output data in step, but got nil or different type")
		}
	} else {
		t.Errorf("Expected execution 2 to have steps with FixedTimeTrigger output")
	}
}
