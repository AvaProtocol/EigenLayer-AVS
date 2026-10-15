package mapping

import (
	"encoding/json"
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestProtoToOpenAPIExecutionIncludesRunSummary(t *testing.T) {
	summary, err := structpb.NewValue(map[string]interface{}{
		"subject": "Batch pay ran",
		"notice":  "kept",
		"body": map[string]interface{}{
			"summary": "Sent 0.25 USDC",
			"executions": []interface{}{
				map[string]interface{}{"description": "Sent 0.1 USDC", "txHash": "0x5de5"},
			},
			"errors": []interface{}{},
		},
	})
	require.NoError(t, err)

	exec := &avsproto.Execution{
		Id:      "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		StartAt: 10,
		Status:  avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS,
		Summary: summary,
	}
	out, err := ProtoToOpenAPIExecution(exec, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.NoError(t, err)
	require.NotNil(t, out.Summary)
	require.NotNil(t, out.Summary.Subject)
	require.Equal(t, "Batch pay ran", *out.Summary.Subject)
	require.NotNil(t, out.Summary.Body)
	require.Len(t, (*out.Summary.Body)["executions"].([]interface{}), 1)

	raw, err := json.Marshal(out.Summary)
	require.NoError(t, err)
	var wire map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &wire))
	require.Equal(t, "Batch pay ran", wire["subject"])
	require.Equal(t, "kept", wire["notice"])
	require.Equal(t, "kept", out.Summary.AdditionalProperties["notice"])
	wireBody, ok := wire["body"].(map[string]interface{})
	require.True(t, ok)
	require.Len(t, wireBody["executions"].([]interface{}), 1)

	status, err := ProtoExecutionToOpenAPISummary(exec, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.NoError(t, err)
	require.Equal(t, out.Summary, status.Summary)

	without, err := ProtoExecutionToOpenAPISummary(&avsproto.Execution{
		Id:      exec.Id,
		StartAt: exec.StartAt,
		Status:  exec.Status,
	}, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.NoError(t, err)
	require.Nil(t, without.Summary)
}

func TestRunSummaryRejectsASubjectThatIsNotAString(t *testing.T) {
	summary, err := structpb.NewValue(map[string]interface{}{
		"subject": float64(1),
		"body":    map[string]interface{}{"summary": "x"},
	})
	require.NoError(t, err)
	out, err := RunSummary(summary)
	require.Error(t, err)
	require.Nil(t, out)

	// The full execution mapper treats it like any other field that does
	// not decode.
	_, err = ProtoToOpenAPIExecution(&avsproto.Execution{
		Id:      "01ARZ3NDEKTSV4RRFFQ69G5FAV",
		StartAt: 10,
		Status:  avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS,
		Summary: summary,
	}, "01ARZ3NDEKTSV4RRFFQ69G5FAV")
	require.ErrorContains(t, err, "summary:")
}
