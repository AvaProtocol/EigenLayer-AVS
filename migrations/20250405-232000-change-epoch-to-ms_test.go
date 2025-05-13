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
	logger := testutil.GetLogger() // Use logger from testutil if needed by setup funcs
	_ = logger                     // Avoid unused variable error if logger isn't directly used
	db := testutil.TestMustDB()
	defer db.Close()

	taskID := "task-123"
	execID := "exec-abc"

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

	sampleTask := &avsproto.Task{
		Id:          taskID,
		StartAt:     startSeconds,     // Seconds
		ExpiredAt:   expiredSeconds,   // Seconds
		CompletedAt: completedSeconds, // Seconds
		LastRanAt:   lastRanSeconds,   // Seconds
	}

	sampleExec := &avsproto.Execution{
		Id:      execID,
		StartAt: execStartSeconds, // Seconds
		EndAt:   execEndSeconds,   // Seconds
		Steps: []*avsproto.Execution_Step{
			{
				NodeId:  "step-1",
				Success: true,
				StartAt: stepStartSeconds, // Seconds
				EndAt:   stepEndSeconds,   // Seconds
			},
		},
		OutputData: &avsproto.Execution_TransferLog{
			TransferLog: &avsproto.Execution_TransferLogOutput{
				BlockTimestamp: uint64(blockTimestampSeconds), // Seconds
			},
		},
	}
	execID2 := "exec-def"
	sampleExec2 := &avsproto.Execution{
		Id:      execID2,
		StartAt: execStartSeconds, // Seconds
		EndAt:   execEndSeconds,   // Seconds
		OutputData: &avsproto.Execution_Time{
			Time: &avsproto.Execution_TimeOutput{
				Epoch: uint64(epochSeconds), // Seconds
			},
		},
	}

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

	taskKey := fmt.Sprintf("t:%s", taskID)
	execKey := fmt.Sprintf("history:%s:%s", taskID, execID)
	execKey2 := fmt.Sprintf("history:%s:%s", taskID, execID2)

	updates := map[string][]byte{
		taskKey:  taskBytes,
		execKey:  execData,
		execKey2: execData2,
	}
	if err := db.BatchWrite(updates); err != nil {
		t.Fatalf("Failed to write initial data to db: %v", err)
	}

	updatedCount, err := ChangeEpochToMs(db)
	if err != nil {
		t.Fatalf("Migration function failed: %v", err)
	}
	expectedUpdates := 3
	if updatedCount != expectedUpdates {
		t.Errorf("Migration reported updating %d records, expected %d", updatedCount, expectedUpdates)
	}


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

	if transferLogOutput := retrievedExec.GetTransferLog(); transferLogOutput != nil {
		if transferLogOutput.BlockTimestamp != uint64(expectedBlockTimestampMs) {
			t.Errorf("TransferLog BlockTimestamp incorrect: got %d, want %d", transferLogOutput.BlockTimestamp, expectedBlockTimestampMs)
		}
	} else {
		t.Errorf("Expected TransferLog output data, but got nil or different type")
	}

	retrievedExecBytes2, err := db.GetKey([]byte(execKey2))
	if err != nil {
		t.Fatalf("Failed to retrieve execution data 2 after migration: %v", err)
	}
	retrievedExec2 := &avsproto.Execution{}
	if err := protojson.Unmarshal(retrievedExecBytes2, retrievedExec2); err != nil {
		t.Fatalf("Failed to unmarshal retrieved execution data 2: %v", err)
	}

	if timeOutput := retrievedExec2.GetTime(); timeOutput != nil {
		if timeOutput.Epoch != uint64(expectedEpochMs) {
			t.Errorf("TimeOutput Epoch incorrect: got %d, want %d", timeOutput.Epoch, expectedEpochMs)
		}
	} else {
		t.Errorf("Expected Time output data, but got nil or different type")
	}
}
