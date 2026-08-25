package rest

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserOpStatusToOpenAPI(t *testing.T) {
	failed := false
	status := &taskengine.UserOpStatus{
		UserOpHash:      "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Sender:          "0x2222222222222222222222222222222222222222",
		ExecutionStatus: "failed",
		TransactionHash: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		BlockNumber:     "0x64",
		Success:         &failed,
		Calls: []taskengine.InnerCall{{
			To:       "0xAaaa000000000000000000000000000000000001",
			Value:    "0",
			Selector: "0xa9059cbb",
			Data:     "0xa9059cbb",
		}},
	}
	status.FailedCall = &status.Calls[0]

	out := userOpStatusToOpenAPI(status)
	assert.Equal(t, status.UserOpHash, string(out.UserOpHash))
	require.NotNil(t, out.Sender)
	assert.Equal(t, status.Sender, string(*out.Sender))
	assert.Equal(t, "failed", string(out.ExecutionStatus))
	require.NotNil(t, out.Calls)
	require.Len(t, *out.Calls, 1)
	assert.Equal(t, "0xa9059cbb", (*out.Calls)[0].Selector)
	require.NotNil(t, out.FailedCall)
	assert.Equal(t, (*out.Calls)[0].To, out.FailedCall.To)
}
