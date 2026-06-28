package mapping

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// TestExecutionStatusProtoToWire pins the proto→wire status vocabulary, including the
// durable-execution WAITING state — without an explicit case it would fall through to
// "pending" and a client could not tell "awaiting approval" from "in-flight".
func TestExecutionStatusProtoToWire(t *testing.T) {
	cases := map[avsproto.ExecutionStatus]string{
		avsproto.ExecutionStatus_EXECUTION_STATUS_PENDING: "pending",
		avsproto.ExecutionStatus_EXECUTION_STATUS_WAITING: "waiting",
		avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS: "success",
		avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED:  "failed",
		avsproto.ExecutionStatus_EXECUTION_STATUS_ERROR:   "error",
	}
	for in, want := range cases {
		assert.Equal(t, want, executionStatusProtoToWire(in), in.String())
	}
	// The wire value is a member of the generated OpenAPI enum (no drift).
	assert.Equal(t, string(generated.ExecutionStatusWaiting), executionStatusProtoToWire(avsproto.ExecutionStatus_EXECUTION_STATUS_WAITING))
}
