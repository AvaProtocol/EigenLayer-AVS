package mapping

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// TestProtoToOpenAPITriggerWorkflow_CopiesErrorAndSteps is the conversion
// the REST :trigger handler uses. The previous inline copy dropped
// TriggerTaskResp.steps, so a blocking failure's step.error (bundler
// "replacement underpriced") never left the gateway.
func TestProtoToOpenAPITriggerWorkflow_CopiesErrorAndSteps(t *testing.T) {
	errMsg := "1 of 2 steps failed: w: eth_sendUserOperation: replacement underpriced"
	startAt := int64(1_700_000_000_000)
	endAt := int64(1_700_000_001_000)
	in := &avsproto.TriggerTaskResp{
		ExecutionId: "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Status:      avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED,
		StartAt:     &startAt,
		EndAt:       &endAt,
		Error:       &errMsg,
		Steps: []*avsproto.Execution_Step{
			{
				Id:      "trigger",
				Type:    avsproto.TriggerType_TRIGGER_TYPE_BLOCK.String(),
				Name:    "blockTrigger",
				Success: true,
			},
			{
				Id:      "w",
				Type:    avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE.String(),
				Name:    "w",
				Success: false,
				Error:   "eth_sendUserOperation: replacement underpriced",
			},
		},
	}

	out, err := ProtoToOpenAPITriggerWorkflow(in)
	require.NoError(t, err)
	assert.Equal(t, "01ARZ3NDEKTSV4RRFFQ69G5FAV", string(out.ExecutionId))
	assert.Equal(t, "failed", string(out.Status))
	require.NotNil(t, out.Error)
	assert.Equal(t, errMsg, *out.Error)
	require.NotNil(t, out.Steps)
	require.Len(t, *out.Steps, 2)

	write := (*out.Steps)[1]
	assert.Equal(t, "w", write.Id)
	assert.False(t, write.Success)
	require.NotNil(t, write.Error)
	assert.Equal(t, "eth_sendUserOperation: replacement underpriced", *write.Error)
}

func TestProtoToOpenAPITriggerWorkflow_SuccessOmitsErrorAndEmptySteps(t *testing.T) {
	in := &avsproto.TriggerTaskResp{
		ExecutionId: "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		Status:      avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS,
	}
	out, err := ProtoToOpenAPITriggerWorkflow(in)
	require.NoError(t, err)
	assert.Equal(t, "success", string(out.Status))
	assert.Nil(t, out.Error)
	assert.Nil(t, out.Steps)
}
