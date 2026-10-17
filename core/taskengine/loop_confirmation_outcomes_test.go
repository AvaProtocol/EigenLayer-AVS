package taskengine

import (
	"testing"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestLoopConfirmationCollectorOutlivesReceiptWait(t *testing.T) {
	for _, configured := range []time.Duration{0, time.Minute, 4 * time.Minute} {
		require.Greater(t, loopIterationTimeout(configured, true), userOpConfirmationTimeout+3*time.Minute)
	}
	require.Equal(t, 12*time.Minute, loopIterationTimeout(12*time.Minute, true))
	require.Equal(t, 30*time.Second, loopIterationTimeout(0, false))
	require.Equal(t, time.Second, loopIterationTimeout(time.Second, false), "off-chain retry cancellation remains bounded by its configured collector")
}

func TestLoopConfirmationOutcomePrioritizesUnresolvedAndPreservesReceipts(t *testing.T) {
	for _, code := range []avsproto.ErrorCode{avsproto.ErrorCode_USER_OP_EXECUTION_REVERTED, avsproto.ErrorCode_USER_OP_CONFIRMATION_UNRESOLVED} {
		t.Run(code.String(), func(t *testing.T) {
			vm := confirmationTestVM(t, func(string) any { return nil })
			pending := pendingConfirmationStep(t)
			pending.Id = "batch_iter_2"
			vm.markUserOpConfirmationUnresolved(pending)
			reverted := pendingConfirmationStep(t)
			reverted.Id = "batch_iter_1"
			methods := reverted.Metadata.AsInterface().([]any)
			reverted.Metadata, _ = structpb.NewValue(methods[:1])
			updateConfirmedWriteMetadata(reverted, confirmationTestHash, &waitForUserOpConfirmationResult{Success: false, FailureReason: "execution reverted: allowance", RevertData: "0xdeadbeef"})
			iterations := []*avsproto.Execution_Step{{Id: "batch_iter_0", Error: "RPC service unavailable"}, reverted}
			if code == avsproto.ErrorCode_USER_OP_CONFIRMATION_UNRESOLVED {
				iterations = append(iterations, pending)
			}
			output, err := structpb.NewValue([]interface{}{nil, nil, nil})
			require.NoError(t, err)
			parent := &avsproto.Execution_Step{OutputData: &avsproto.Execution_Step_Loop{Loop: &avsproto.LoopNode_Output{Data: output}}}
			finalizeStep(parent, false, NewInvalidRequestError("iterations failed: RPC service unavailable"), "", "")
			propagateLoopConfirmationFailure(parent, iterations)
			require.False(t, parent.Success)
			require.Equal(t, code, parent.ErrorCode)
			require.NotContains(t, parent.Error, "RPC service unavailable", "the selected code and reason must describe the same iteration")
			require.Same(t, output, parent.GetLoop().Data, "partial iteration output is retained")
			outcomes := parent.Metadata.AsInterface().(map[string]interface{})["confirmationOutcomes"].([]interface{})
			receipt := outcomes[0].(map[string]interface{})["metadata"].([]interface{})[0].(map[string]interface{})["receipt"].(map[string]interface{})
			require.Equal(t, "0xdeadbeef", receipt["revertData"])
			if code == avsproto.ErrorCode_USER_OP_CONFIRMATION_UNRESOLVED {
				require.Contains(t, parent.Error, "batch_iter_2")
				require.Contains(t, parent.Error, "check its status before retrying")
				methods := outcomes[1].(map[string]interface{})["metadata"].([]interface{})
				require.Equal(t, confirmationTestHash, methods[0].(map[string]interface{})["receipt"].(map[string]interface{})["userOpHash"])
			}
		})
	}
}

func TestLoopWithoutConfirmationFailureKeepsOrdinaryFailure(t *testing.T) {
	parent := &avsproto.Execution_Step{Error: "iterations failed: bad input", ErrorCode: avsproto.ErrorCode_INVALID_REQUEST}
	propagateLoopConfirmationFailure(parent, []*avsproto.Execution_Step{nil, {Error: "bad input", ErrorCode: avsproto.ErrorCode_INVALID_REQUEST}})
	require.Equal(t, avsproto.ErrorCode_INVALID_REQUEST, parent.ErrorCode)
	require.Equal(t, "iterations failed: bad input", parent.Error)
	require.Nil(t, parent.Metadata)
}
