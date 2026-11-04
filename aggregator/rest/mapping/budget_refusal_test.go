package mapping

import (
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestBudgetRefusalFieldsReachRESTStep(t *testing.T) {
	details, err := structpb.NewStruct(map[string]interface{}{"token": "0x0000000000000000000000000000000000000001", "needed": "10000000000000000000000000", "remaining": "0"})
	require.NoError(t, err)
	out, err := protoExecutionStepToOpenAPI(&avsproto.Execution_Step{Id: "write", ErrorCode: avsproto.ErrorCode_BUDGET_USED_UP, ErrorDetails: details})
	require.NoError(t, err)
	require.Equal(t, "BUDGET_USED_UP", *out.ErrorCode)
	require.NotNil(t, out.ErrorDetails)
	require.Equal(t, "10000000000000000000000000", *out.ErrorDetails.Needed)
	details, err = structpb.NewStruct(map[string]interface{}{"validUntil": int64(1234567890000)})
	require.NoError(t, err)
	out, err = protoExecutionStepToOpenAPI(&avsproto.Execution_Step{Id: "write", ErrorCode: avsproto.ErrorCode_BUDGET_ENDED, ErrorDetails: details})
	require.NoError(t, err)
	require.EqualValues(t, 1234567890000, *out.ErrorDetails.ValidUntil)
}
