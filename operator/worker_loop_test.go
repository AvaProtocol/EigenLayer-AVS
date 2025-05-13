package operator

import (
	"context"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/trigger"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avspb "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

type MockTrigger struct {
	mock.Mock
}

func (m *MockTrigger) RemoveCheck(taskID string) {
	m.Called(taskID)
}

func (m *MockTrigger) AddCheck(taskMetadata *avspb.SyncMessagesResp_TaskMetadata) error {
	args := m.Called(taskMetadata)
	return args.Error(0)
}

func (m *MockTrigger) GetProgress() int64 {
	args := m.Called()
	return args.Get(0).(int64)
}

func (m *MockTrigger) Run(ctx context.Context) {
	m.Called(ctx)
}

func TestTaskRemovalFromAllTriggers(t *testing.T) {
	mockEventTrigger := new(MockTrigger)
	mockBlockTrigger := new(MockTrigger)
	mockTimeTrigger := new(MockTrigger)

	operator := &Operator{
		logger:       testutil.GetLogger(),
		eventTrigger: mockEventTrigger,
		blockTrigger: mockBlockTrigger,
		timeTrigger:  mockTimeTrigger,
	}

	testCases := []struct {
		name          string
		op            avspb.MessageOp
		taskID        string
		expectRemoval bool
	}{
		{
			name:          "Cancel Task",
			op:            avspb.MessageOp_CancelTask,
			taskID:        "task-123",
			expectRemoval: true,
		},
		{
			name:          "Delete Task",
			op:            avspb.MessageOp_DeleteTask,
			taskID:        "task-456",
			expectRemoval: true,
		},
		{
			name:          "Monitor Task",
			op:            avspb.MessageOp_MonitorTaskTrigger,
			taskID:        "task-789",
			expectRemoval: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			message := &avspb.SyncMessagesResp{
				Op: tc.op,
				TaskMetadata: &avspb.SyncMessagesResp_TaskMetadata{
					TaskId: tc.taskID,
				},
			}

			if tc.expectRemoval {
				mockEventTrigger.On("RemoveCheck", tc.taskID).Return()
				mockBlockTrigger.On("RemoveCheck", tc.taskID).Return()
				mockTimeTrigger.On("RemoveCheck", tc.taskID).Return()
			}

			operator.processMessage(message)

			if tc.expectRemoval {
				mockEventTrigger.AssertCalled(t, "RemoveCheck", tc.taskID)
				mockBlockTrigger.AssertCalled(t, "RemoveCheck", tc.taskID)
				mockTimeTrigger.AssertCalled(t, "RemoveCheck", tc.taskID)
			} else {
				mockEventTrigger.AssertNotCalled(t, "RemoveCheck", tc.taskID)
				mockBlockTrigger.AssertNotCalled(t, "RemoveCheck", tc.taskID)
				mockTimeTrigger.AssertNotCalled(t, "RemoveCheck", tc.taskID)
			}
		})
	}
}

func TestTaskRemovalFromAllTriggersIntegration(t *testing.T) {
	t.Skip("Integration test requires complex setup with mocked gRPC streams")
}
