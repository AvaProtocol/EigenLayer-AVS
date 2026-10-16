package mapping

import (
	"encoding/json"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestExecutionRESTPreservesContractWriteConfirmationMetadata(t *testing.T) {
	for _, code := range []avsproto.ErrorCode{avsproto.ErrorCode_USER_OP_CONFIRMATION_UNRESOLVED, avsproto.ErrorCode_USER_OP_EXECUTION_REVERTED} {
		t.Run(code.String(), func(t *testing.T) {
			metadata, err := structpb.NewValue([]interface{}{map[string]interface{}{
				"methodName": "approve", "success": false, "receipt": map[string]interface{}{
					"userOpHash": "0xuserop", "executionStatus": "failed", "revertData": "0xdeadbeef",
				},
			}})
			require.NoError(t, err)
			step := &avsproto.Execution_Step{Id: "write", Type: "NODE_TYPE_CONTRACT_WRITE", Error: "confirmation outcome", ErrorCode: code, Metadata: metadata}
			execution, err := ProtoToOpenAPIExecution(&avsproto.Execution{Id: "execution", Steps: []*avsproto.Execution_Step{step}}, "workflow")
			require.NoError(t, err)
			trigger, err := ProtoToOpenAPITriggerWorkflow(&avsproto.TriggerTaskResp{ExecutionId: "execution", Steps: []*avsproto.Execution_Step{step}})
			require.NoError(t, err)
			for _, response := range []interface{}{execution, trigger} {
				raw, err := json.Marshal(response)
				require.NoError(t, err)
				var wire map[string]interface{}
				require.NoError(t, json.Unmarshal(raw, &wire))
				wireStep := wire["steps"].([]interface{})[0].(map[string]interface{})
				require.Equal(t, code.String(), wireStep["errorCode"])
				results := wireStep["metadata"].(map[string]interface{})["results"].([]interface{})
				receipt := results[0].(map[string]interface{})["receipt"].(map[string]interface{})
				require.Equal(t, "0xuserop", receipt["userOpHash"])
				require.Equal(t, "failed", receipt["executionStatus"])
				require.Equal(t, "0xdeadbeef", receipt["revertData"])
			}
		})
	}
}

func TestExecutionRESTKeepsEmptyResultsAndObjectMetadata(t *testing.T) {
	for _, value := range []interface{}{[]interface{}{}, map[string]interface{}{"userOpHash": "0xpending", "receiptStatus": "pending"}} {
		metadata, err := structpb.NewValue(value)
		require.NoError(t, err)
		out, err := ProtoToOpenAPIExecution(&avsproto.Execution{Steps: []*avsproto.Execution_Step{{Id: "step", Metadata: metadata}}}, "workflow")
		require.NoError(t, err)
		got := *(*out.Steps)[0].Metadata
		if original, ok := value.(map[string]interface{}); ok {
			require.Equal(t, original, got)
		} else {
			require.Equal(t, []interface{}{}, got["results"])
		}
	}
}
