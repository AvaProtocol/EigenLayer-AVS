package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"google.golang.org/protobuf/types/known/structpb"
)

// Verifies that non-blocking BLOCK TriggerTask pre-creates an execution id,
// persists Pending status, and exposes it via GetExecutionStatus
func TestTriggerTask_NonBlockingBlock_PersistsPendingAndReturnsId(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	// Create a simple REST task (nodes don't matter for trigger pre-creation)
	tr := testutil.RestTask()
	// Ensure we have a smart wallet address for the task
	walletResp, err := n.GetWallet(testutil.TestUser1(), &avsproto.GetWalletReq{Salt: "12345"})
	if err != nil {
		t.Fatalf("Failed to get wallet: %v", err)
	}
	tr.SmartWalletAddress = walletResp.Address

	created, err := n.CreateTask(testutil.TestUser1(), tr)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	// Trigger with non-blocking BLOCK
	resp, err := n.TriggerTask(testutil.TestUser1(), &avsproto.TriggerTaskReq{
		TaskId:      created.Id,
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		TriggerOutput: &avsproto.TriggerTaskReq_BlockTrigger{
			BlockTrigger: &avsproto.BlockTrigger_Output{
				Data: func() *structpb.Value {
					v, _ := structpb.NewValue(map[string]interface{}{"blockNumber": 101})
					return v
				}(),
			},
		},
		IsBlocking: false,
	})
	if err != nil {
		t.Fatalf("TriggerTask (non-blocking block) failed: %v", err)
	}
	if resp == nil || resp.ExecutionId == "" {
		t.Fatalf("Expected non-empty execution id, got: %+v", resp)
	}
	if resp.Status != avsproto.ExecutionStatus_EXECUTION_STATUS_PENDING {
		t.Fatalf("Expected status PENDING in response, got %v", resp.Status)
	}

	// Verify GetExecutionStatus returns Pending
	status, err := n.GetExecutionStatus(testutil.TestUser1(), &avsproto.ExecutionReq{
		TaskId:      created.Id,
		ExecutionId: resp.ExecutionId,
	})
	if err != nil {
		t.Fatalf("GetExecutionStatus failed: %v", err)
	}
	if status.Status != avsproto.ExecutionStatus_EXECUTION_STATUS_PENDING {
		t.Fatalf("Expected GetExecutionStatus=PENDING, got %v", status.Status)
	}
}
