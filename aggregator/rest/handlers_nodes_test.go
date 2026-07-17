package rest

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// contractWrite surfaces its per-method results (including the receipt with
// executionStatus / userOpHash / transactionHash) as an ARRAY-typed metadata.
// runNodeRespToOpenAPI must wrap that array under "results" so the client can
// read it — a bare array used to be silently dropped, taking the receipts with it.
func TestRunNodeRespToOpenAPISurfacesContractWriteReceiptArray(t *testing.T) {
	results := []interface{}{
		map[string]interface{}{
			"methodName": "exactInputSingle",
			"success":    true,
			"receipt": map[string]interface{}{
				"executionStatus": "confirmed",
				"userOpHash":      "0xuserop",
				"transactionHash": "0xtx",
			},
		},
	}
	md, err := structpb.NewValue(results)
	require.NoError(t, err)

	out := runNodeRespToOpenAPI(&avsproto.RunNodeWithInputsResp{Success: true, Metadata: md})

	require.NotNil(t, out.Metadata, "array metadata must be surfaced, not dropped")
	m := *out.Metadata
	arr, ok := m["results"].([]interface{})
	require.True(t, ok, "array metadata must be wrapped under 'results'")
	require.Len(t, arr, 1)
	first := arr[0].(map[string]interface{})
	receipt := first["receipt"].(map[string]interface{})
	assert.Equal(t, "confirmed", receipt["executionStatus"])
	assert.Equal(t, "0xuserop", receipt["userOpHash"])
	assert.Equal(t, "0xtx", receipt["transactionHash"])
}

// An EMPTY results array must still surface as {results: []}, not metadata=null,
// so clients see a consistent shape for a legitimately empty result set.
func TestRunNodeRespToOpenAPIEmptyArrayMetadataWrapped(t *testing.T) {
	md, err := structpb.NewValue([]interface{}{})
	require.NoError(t, err)

	out := runNodeRespToOpenAPI(&avsproto.RunNodeWithInputsResp{Success: true, Metadata: md})

	require.NotNil(t, out.Metadata, "empty array metadata must still be surfaced")
	m := *out.Metadata
	arr, ok := m["results"].([]interface{})
	require.True(t, ok, "empty array must be wrapped under 'results'")
	assert.Empty(t, arr)
}

// Map-typed metadata (e.g. ethTransfer's gasUsed/transactionHash) must keep
// surfacing as-is, without the "results" wrapper.
func TestRunNodeRespToOpenAPIMapMetadataStillForwarded(t *testing.T) {
	md, err := structpb.NewValue(map[string]interface{}{"transactionHash": "0xabc", "gasUsed": "21000"})
	require.NoError(t, err)

	out := runNodeRespToOpenAPI(&avsproto.RunNodeWithInputsResp{Success: true, Metadata: md})

	require.NotNil(t, out.Metadata)
	m := *out.Metadata
	assert.Equal(t, "0xabc", m["transactionHash"])
	assert.Nil(t, m["results"], "map metadata is forwarded as-is, not wrapped")
}
