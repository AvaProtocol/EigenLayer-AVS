package migrations

import (
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/oklog/ulid/v2"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestAddExecutionIndexes(t *testing.T) {
	// Set up test database
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	// Create test data - multiple tasks with multiple executions each
	testTaskID1 := "01H000000000000000000000Z1"
	testTaskID2 := "01H000000000000000000000Z2"

	// Create executions with different timestamps (simulating creation over time)
	// Task 1: 3 executions
	executions1 := []struct {
		executionID string
		timestamp   time.Time
	}{
		{"01H000000000000000000001E1", time.Now().Add(-3 * time.Hour)}, // Oldest
		{"01H000000000000000000001E2", time.Now().Add(-2 * time.Hour)}, // Middle
		{"01H000000000000000000001E3", time.Now().Add(-1 * time.Hour)}, // Newest
	}

	// Task 2: 2 executions
	executions2 := []struct {
		executionID string
		timestamp   time.Time
	}{
		{"01H000000000000000000002E1", time.Now().Add(-30 * time.Minute)}, // Older
		{"01H000000000000000000002E2", time.Now().Add(-10 * time.Minute)}, // Newer
	}

	// Store executions in the database (all with Index = 0, simulating old data)
	totalExecutions := 0

	// Store Task 1 executions
	for _, exec := range executions1 {
		execution := &avsproto.Execution{
			Id:      exec.executionID,
			StartAt: exec.timestamp.UnixMilli(),
			EndAt:   exec.timestamp.Add(time.Minute).UnixMilli(),
			Status:  avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS,
			Index:   0, // Old data doesn't have proper indexes
			Steps: []*avsproto.Execution_Step{
				{
					Id:      "step-1",
					Success: true,
					Name:    "test-step",
				},
			},
		}

		key := "history:" + testTaskID1 + ":" + exec.executionID
		data, err := protojson.Marshal(execution)
		if err != nil {
			t.Fatalf("Failed to marshal execution: %v", err)
		}

		err = db.Set([]byte(key), data)
		if err != nil {
			t.Fatalf("Failed to store execution: %v", err)
		}
		totalExecutions++
	}

	// Store Task 2 executions
	for _, exec := range executions2 {
		execution := &avsproto.Execution{
			Id:      exec.executionID,
			StartAt: exec.timestamp.UnixMilli(),
			EndAt:   exec.timestamp.Add(time.Minute).UnixMilli(),
			Status:  avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED,
			Index:   0, // Old data doesn't have proper indexes
			Steps: []*avsproto.Execution_Step{
				{
					Id:      "step-1",
					Success: false,
					Error:   "test error",
					Name:    "test-step",
				},
			},
		}

		key := "history:" + testTaskID2 + ":" + exec.executionID
		data, err := protojson.Marshal(execution)
		if err != nil {
			t.Fatalf("Failed to marshal execution: %v", err)
		}

		err = db.Set([]byte(key), data)
		if err != nil {
			t.Fatalf("Failed to store execution: %v", err)
		}
		totalExecutions++
	}

	t.Logf("Created %d test executions across 2 tasks", totalExecutions)

	// Run the migration
	recordsUpdated, err := AddExecutionIndexes(db)
	if err != nil {
		t.Fatalf("Migration failed: %v", err)
	}

	// Verify the migration updated the expected number of records
	if recordsUpdated != totalExecutions {
		t.Errorf("Expected %d records to be updated, got %d", totalExecutions, recordsUpdated)
	}

	// Verify Task 1 executions have correct indexes (should be 0, 1, 2 in chronological order)
	task1ExpectedIndexes := []struct {
		executionID   string
		expectedIndex int64
	}{
		{"01H000000000000000000001E1", 0}, // Oldest execution gets index 0
		{"01H000000000000000000001E2", 1}, // Middle execution gets index 1
		{"01H000000000000000000001E3", 2}, // Newest execution gets index 2
	}

	for _, expected := range task1ExpectedIndexes {
		key := "history:" + testTaskID1 + ":" + expected.executionID
		data, err := db.GetKey([]byte(key))
		if err != nil {
			t.Fatalf("Failed to retrieve execution %s: %v", expected.executionID, err)
		}

		var execution avsproto.Execution
		if err := protojson.Unmarshal(data, &execution); err != nil {
			t.Fatalf("Failed to unmarshal execution %s: %v", expected.executionID, err)
		}

		if execution.Index != expected.expectedIndex {
			t.Errorf("Task 1 execution %s has incorrect index: expected %d, got %d",
				expected.executionID, expected.expectedIndex, execution.Index)
		}
	}

	// Verify Task 2 executions have correct indexes (should be 0, 1 in chronological order)
	task2ExpectedIndexes := []struct {
		executionID   string
		expectedIndex int64
	}{
		{"01H000000000000000000002E1", 0}, // Older execution gets index 0
		{"01H000000000000000000002E2", 1}, // Newer execution gets index 1
	}

	for _, expected := range task2ExpectedIndexes {
		key := "history:" + testTaskID2 + ":" + expected.executionID
		data, err := db.GetKey([]byte(key))
		if err != nil {
			t.Fatalf("Failed to retrieve execution %s: %v", expected.executionID, err)
		}

		var execution avsproto.Execution
		if err := protojson.Unmarshal(data, &execution); err != nil {
			t.Fatalf("Failed to unmarshal execution %s: %v", expected.executionID, err)
		}

		if execution.Index != expected.expectedIndex {
			t.Errorf("Task 2 execution %s has incorrect index: expected %d, got %d",
				expected.executionID, expected.expectedIndex, execution.Index)
		}
	}

	t.Logf("✅ Migration test passed - all executions have correct sequential indexes")
}

func TestAddExecutionIndexes_EmptyDatabase(t *testing.T) {
	// Set up empty test database
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	// Run the migration on empty database
	recordsUpdated, err := AddExecutionIndexes(db)
	if err != nil {
		t.Fatalf("Migration failed on empty database: %v", err)
	}

	// Should return 0 records updated
	if recordsUpdated != 0 {
		t.Errorf("Expected 0 records updated on empty database, got %d", recordsUpdated)
	}

	t.Logf("✅ Empty database test passed")
}

func TestAddExecutionIndexes_RealULIDs(t *testing.T) {
	// Test with real ULIDs to ensure proper sorting
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	testTaskID := "01H000000000000000000000Z3"

	// Generate real ULIDs with different timestamps
	baseTime := time.Now().Add(-1 * time.Hour)
	executions := []ulid.ULID{
		ulid.MustNew(ulid.Timestamp(baseTime.Add(0*time.Minute)), nil),
		ulid.MustNew(ulid.Timestamp(baseTime.Add(10*time.Minute)), nil),
		ulid.MustNew(ulid.Timestamp(baseTime.Add(20*time.Minute)), nil),
		ulid.MustNew(ulid.Timestamp(baseTime.Add(30*time.Minute)), nil),
	}

	// Store executions in random order (not chronological)
	randomOrder := []int{2, 0, 3, 1} // Random order to test sorting
	for i, randomIndex := range randomOrder {
		executionULID := executions[randomIndex]
		execution := &avsproto.Execution{
			Id:      executionULID.String(),
			StartAt: ulid.Time(executionULID.Time()).UnixMilli(),
			EndAt:   ulid.Time(executionULID.Time()).Add(time.Minute).UnixMilli(),
			Status:  avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS,
			Index:   0, // All start with index 0
			Steps: []*avsproto.Execution_Step{
				{
					Id:      "step-1",
					Success: true,
					Name:    "test-step",
				},
			},
		}

		key := "history:" + testTaskID + ":" + executionULID.String()
		data, err := protojson.Marshal(execution)
		if err != nil {
			t.Fatalf("Failed to marshal execution %d: %v", i, err)
		}

		err = db.Set([]byte(key), data)
		if err != nil {
			t.Fatalf("Failed to store execution %d: %v", i, err)
		}
	}

	// Run migration
	recordsUpdated, err := AddExecutionIndexes(db)
	if err != nil {
		t.Fatalf("Migration failed: %v", err)
	}

	if recordsUpdated != 4 {
		t.Errorf("Expected 4 records updated, got %d", recordsUpdated)
	}

	// Verify executions are indexed in chronological order (0, 1, 2, 3)
	for expectedIndex, executionULID := range executions {
		key := "history:" + testTaskID + ":" + executionULID.String()
		data, err := db.GetKey([]byte(key))
		if err != nil {
			t.Fatalf("Failed to retrieve execution %s: %v", executionULID.String(), err)
		}

		var execution avsproto.Execution
		if err := protojson.Unmarshal(data, &execution); err != nil {
			t.Fatalf("Failed to unmarshal execution %s: %v", executionULID.String(), err)
		}

		if execution.Index != int64(expectedIndex) {
			t.Errorf("Execution %s has incorrect index: expected %d, got %d",
				executionULID.String(), expectedIndex, execution.Index)
		}
	}

	t.Logf("✅ Real ULID sorting test passed")
}
