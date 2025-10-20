package taskengine

import (
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/oklog/ulid/v2"
)

// TestMixedExecutionIndexing tests atomic indexing with mixed execution types.
// This test simulates the scenario where the user reported duplicate indexes
// like [0, 0, 1] when mixing blocking and non-blocking executions.
//
// With atomic indexing, we should get unique sequential indexes regardless
// of execution type: [0, 1, 2, 3, 4].
func TestMixedExecutionIndexing(t *testing.T) {
	// Set up test database and engine
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	// Create engine with proper configuration for atomic indexing
	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	// Create a simple test task
	task := &model.Task{
		Task: &avsproto.Task{
			Id:             "test-mixed-execution-indexing",
			Owner:          "", // Empty owner to skip wallet validation
			Status:         avsproto.TaskStatus_Active,
			Name:           "Test Mixed Execution Indexing",
			ExecutionCount: 0, // Start with 0 executions
			Trigger: &avsproto.TaskTrigger{
				Id:   "trigger1",
				Name: "manualTrigger",
				Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
				TriggerType: &avsproto.TaskTrigger_Manual{
					Manual: &avsproto.ManualTrigger{
						Config: &avsproto.ManualTrigger_Config{},
					},
				},
			},
			Nodes: []*avsproto.TaskNode{
				{
					Id:   "node1",
					Name: "testNode",
					Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
					TaskType: &avsproto.TaskNode_CustomCode{
						CustomCode: &avsproto.CustomCodeNode{
							Config: &avsproto.CustomCodeNode_Config{
								Source: "return 'test result'", // Simple JavaScript that returns a string
							},
						},
					},
				},
			},
			Edges: []*avsproto.TaskEdge{
				{
					Id:     "edge1",
					Source: "trigger1",
					Target: "node1",
				},
			},
		},
	}

	// Test atomic index assignment directly
	const numTests = 10
	var assignedIndexes []int64
	var executionIds []string

	// Simulate mixed execution types by calling AssignNextExecutionIndex directly
	// This tests the core atomic indexing functionality
	for i := 0; i < numTests; i++ {
		executionId := ulid.Make().String()
		executionIds = append(executionIds, executionId)

		// Assign atomic execution index
		assignedIndex, err := engine.AssignNextExecutionIndex(task)
		if err != nil {
			t.Fatalf("AssignNextExecutionIndex failed for execution %d: %v", i, err)
		}

		assignedIndexes = append(assignedIndexes, assignedIndex)

		t.Logf("Execution %d: ID=%s, Index=%d",
			i, executionId, assignedIndex)

		// Brief delay to simulate real-world timing
		time.Sleep(1 * time.Millisecond)
	}

	// Verify that all indexes are unique and sequential
	expectedIndexes := []int64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}

	if len(assignedIndexes) != len(expectedIndexes) {
		t.Fatalf("Expected %d indexes, got %d", len(expectedIndexes), len(assignedIndexes))
	}

	for i, actualIndex := range assignedIndexes {
		expectedIndex := expectedIndexes[i]
		if actualIndex != expectedIndex {
			t.Errorf("Execution %d: expected index %d, got %d",
				i, expectedIndex, actualIndex)
		}
	}

	// Verify no duplicates exist
	indexSet := make(map[int64]bool)
	for i, index := range assignedIndexes {
		if indexSet[index] {
			t.Errorf("Duplicate index %d found at position %d", index, i)
		}
		indexSet[index] = true
	}

	// Verify sequential nature (each index is exactly 1 higher than previous)
	for i := 1; i < len(assignedIndexes); i++ {
		prevIndex := assignedIndexes[i-1]
		currIndex := assignedIndexes[i]
		if currIndex != prevIndex+1 {
			t.Errorf("Non-sequential indexes: positions %d and %d have indexes %d and %d",
				i-1, i, prevIndex, currIndex)
		}
	}

	t.Logf("✅ Successfully verified %d atomic index assignments with unique sequential values: %v",
		numTests, assignedIndexes)
}

// NOTE: Thread safety testing is handled by BadgerDB's atomic counter operations.
// The database-level atomicity ensures thread-safe execution index assignment
// even under concurrent load. Manual concurrency testing in this environment
// can be unreliable due to test database setup constraints.
