package mapping

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// TestExecutionStatusProtoToWire pins the proto→wire status vocabulary, including the
// durable-execution WAITING state — without an explicit case it would fall through to
// the default and a client could not tell "awaiting approval" from "in-flight". The
// coverage loop fails if a new ExecutionStatus enum value is added without a mapping
// (exactly the miss that once let WAITING surface as "pending").
func TestExecutionStatusProtoToWire(t *testing.T) {
	expected := map[avsproto.ExecutionStatus]string{
		avsproto.ExecutionStatus_EXECUTION_STATUS_UNSPECIFIED: "pending",
		avsproto.ExecutionStatus_EXECUTION_STATUS_PENDING:     "pending",
		avsproto.ExecutionStatus_EXECUTION_STATUS_WAITING:     "waiting",
		avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS:     "success",
		avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED:      "failed",
		avsproto.ExecutionStatus_EXECUTION_STATUS_ERROR:       "error",
	}
	// Every defined proto enum value must have an explicit, asserted mapping — a new
	// value with no entry here fails the test instead of silently hitting the default.
	for v := range avsproto.ExecutionStatus_name {
		s := avsproto.ExecutionStatus(v)
		want, ok := expected[s]
		require.Truef(t, ok, "ExecutionStatus %s has no proto→wire mapping/test entry", s)
		assert.Equal(t, want, executionStatusProtoToWire(s), s.String())
	}
	// The waiting wire value is a member of the generated OpenAPI enum (no drift).
	assert.Equal(t, string(generated.ExecutionStatusWaiting), executionStatusProtoToWire(avsproto.ExecutionStatus_EXECUTION_STATUS_WAITING))
}
