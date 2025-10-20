package taskengine

import (
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestNonBlockingExecutionStability verifies the fix for the SDK test:
// "should maintain stable executionId from trigger response to getExecution for non-blocking"
//
// This test validates that:
// 1. Non-blocking trigger calls return an immediate executionId
// 2. GetExecution with that executionId returns the same stable executionId
// 3. The index is pre-assigned and stable (not recalculated on each GetExecution call)
// 4. Multiple calls to GetExecution return consistent data
func TestNonBlockingExecutionStability(t *testing.T) {
	// Setup test environment
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	user := testutil.TestUser1()

	// Create a wallet for the task
	walletResp, err := engine.GetWallet(user, &avsproto.GetWalletReq{Salt: "12345"})
	require.NoError(t, err)

	// Create a simple block trigger task using testutil pattern
	taskReq := &avsproto.CreateTaskReq{
		Trigger: &avsproto.TaskTrigger{
			Id:   "block_trigger",
			Name: "blockTrigger",
			Type: avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
			TriggerType: &avsproto.TaskTrigger_Block{
				Block: &avsproto.BlockTrigger{
					Config: &avsproto.BlockTrigger_Config{
						Interval: 5, // Every 5 blocks
					},
				},
			},
		},
		Nodes: []*avsproto.TaskNode{
			{
				Id:   "custom_code_node",
				Name: "customCode",
				Type: avsproto.NodeType_NODE_TYPE_CUSTOM_CODE,
				TaskType: &avsproto.TaskNode_CustomCode{
					CustomCode: &avsproto.CustomCodeNode{
						Config: &avsproto.CustomCodeNode_Config{
							Lang:   avsproto.Lang_LANG_JAVASCRIPT,
							Source: "return { foo: 'bar' };",
						},
					},
				},
			},
		},
		Edges: []*avsproto.TaskEdge{
			{
				Id:     "trigger_to_node",
				Source: "block_trigger",
				Target: "custom_code_node",
			},
		},
		SmartWalletAddress: walletResp.Address,
		StartAt:            time.Now().UnixMilli(),
		ExpiredAt:          time.Now().Add(24 * time.Hour).UnixMilli(),
		MaxExecution:       0, // Unlimited executions
		Name:               "Non-blocking Stability Test",
	}

	// Create the task
	createdTask, err := engine.CreateTask(user, taskReq)
	require.NoError(t, err)
	require.NotNil(t, createdTask)

	t.Logf("Created task with ID: %s", createdTask.Id)

	// Trigger non-blocking execution (this is the key test case)
	triggerResp, err := engine.TriggerTask(user, &avsproto.TriggerTaskReq{
		TaskId:      createdTask.Id,
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		TriggerOutput: &avsproto.TriggerTaskReq_BlockTrigger{
			BlockTrigger: &avsproto.BlockTrigger_Output{
				Data: func() *structpb.Value {
					data, _ := structpb.NewValue(map[string]interface{}{
						"blockNumber": 9449900,
						"blockHash":   "0x123...",
					})
					return data
				}(),
			},
		},
		IsBlocking: false, // Non-blocking - should return immediate executionId
	})

	require.NoError(t, err, "TriggerTask (non-blocking) should not return error")
	require.NotNil(t, triggerResp, "TriggerTask response should not be nil")
	require.NotEmpty(t, triggerResp.ExecutionId, "ExecutionId should be immediately available")
	require.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_PENDING, triggerResp.Status, "Status should be PENDING")

	originalExecutionId := triggerResp.ExecutionId
	t.Logf("Immediate executionId from trigger: %s", originalExecutionId)

	// Test 1: GetExecution should return the same stable executionId
	execution1, err := engine.GetExecution(user, &avsproto.ExecutionReq{
		TaskId:      createdTask.Id,
		ExecutionId: originalExecutionId,
	})
	require.NoError(t, err, "GetExecution should not return error")
	require.NotNil(t, execution1, "Execution should not be nil")
	assert.Equal(t, originalExecutionId, execution1.Id, "ExecutionId should remain stable")
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_PENDING, execution1.Status, "Status should still be PENDING")

	// The index should be assigned (not -1 placeholder) and be a valid non-negative number
	assert.GreaterOrEqual(t, execution1.Index, int64(0), "Index should be non-negative")
	originalIndex := execution1.Index
	t.Logf("Pre-assigned index from GetExecution: %d", originalIndex)

	// Test 2: Multiple calls to GetExecution should return consistent data
	execution2, err := engine.GetExecution(user, &avsproto.ExecutionReq{
		TaskId:      createdTask.Id,
		ExecutionId: originalExecutionId,
	})
	require.NoError(t, err, "Second GetExecution should not return error")
	require.NotNil(t, execution2, "Second execution should not be nil")
	assert.Equal(t, originalExecutionId, execution2.Id, "ExecutionId should remain stable on second call")
	assert.Equal(t, originalIndex, execution2.Index, "Index should remain stable on second call")
	assert.Equal(t, execution1.Status, execution2.Status, "Status should be consistent")

	// Test 3: GetExecutionStatus should also work with the same executionId
	statusResp, err := engine.GetExecutionStatus(user, &avsproto.ExecutionReq{
		TaskId:      createdTask.Id,
		ExecutionId: originalExecutionId,
	})
	require.NoError(t, err, "GetExecutionStatus should not return error")
	require.NotNil(t, statusResp, "Status response should not be nil")
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_PENDING, statusResp.Status, "Status should be PENDING")

	// Test 4: Verify that another non-blocking trigger gets a different executionId and incremented index
	triggerResp2, err := engine.TriggerTask(user, &avsproto.TriggerTaskReq{
		TaskId:      createdTask.Id,
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		TriggerOutput: &avsproto.TriggerTaskReq_BlockTrigger{
			BlockTrigger: &avsproto.BlockTrigger_Output{
				Data: func() *structpb.Value {
					data, _ := structpb.NewValue(map[string]interface{}{
						"blockNumber": 9449901,
						"blockHash":   "0x456...",
					})
					return data
				}(),
			},
		},
		IsBlocking: false,
	})

	require.NoError(t, err, "Second TriggerTask should not return error")
	require.NotNil(t, triggerResp2, "Second trigger response should not be nil")
	require.NotEmpty(t, triggerResp2.ExecutionId, "Second ExecutionId should be available")
	assert.NotEqual(t, originalExecutionId, triggerResp2.ExecutionId, "Second ExecutionId should be different")

	// Get the second execution and verify its index
	execution3, err := engine.GetExecution(user, &avsproto.ExecutionReq{
		TaskId:      createdTask.Id,
		ExecutionId: triggerResp2.ExecutionId,
	})
	require.NoError(t, err, "GetExecution for second execution should not return error")
	require.NotNil(t, execution3, "Second execution should not be nil")
	assert.Equal(t, triggerResp2.ExecutionId, execution3.Id, "Second ExecutionId should be stable")
	assert.Equal(t, originalIndex+1, execution3.Index, "Second execution should have incremented index")

	t.Logf("✅ Non-blocking execution stability verified:")
	t.Logf("   - First execution: ID=%s, Index=%d", originalExecutionId, originalIndex)
	t.Logf("   - Second execution: ID=%s, Index=%d", triggerResp2.ExecutionId, execution3.Index)
	t.Logf("   - ExecutionIds remain stable across GetExecution calls")
	t.Logf("   - Indexes are pre-assigned and increment properly")
}
