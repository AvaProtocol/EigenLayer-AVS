package mapping

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// TestProtoToOpenAPINode_PerNodeChainId covers the proto→OpenAPI render path
// (ProtoToOpenAPINode → protoRetargetJSON) that create/get/list use to return a
// persisted node to clients. protojson encodes the int64 chainId as a quoted
// string ("11155111"); without the descriptor-driven unquote (issue #639) the
// generated struct's plain int64 ChainId field rejects it and the whole render
// fails. #633 only exercised the inbound (OpenAPIToProtoNode) direction, which is
// lenient via protojson.Unmarshal, so it stayed green while this path was broken.
func TestProtoToOpenAPINode_PerNodeChainId(t *testing.T) {
	const chainID = int64(11155111)
	const addr = "0x1234567890123456789012345678901234567890"

	t.Run("contractRead", func(t *testing.T) {
		pn := &avsproto.TaskNode{Id: "cr1", Type: avsproto.NodeType_NODE_TYPE_CONTRACT_READ, TaskType: &avsproto.TaskNode_ContractRead{
			ContractRead: &avsproto.ContractReadNode{Config: &avsproto.ContractReadNode_Config{
				ContractAddress: addr,
				ChainId:         chainID,
			}},
		}}
		out, err := ProtoToOpenAPINode(pn)
		require.NoError(t, err)
		v, err := out.AsContractReadNode()
		require.NoError(t, err)
		assert.Equal(t, chainID, int64(v.Config.ChainId))
	})

	t.Run("contractWrite", func(t *testing.T) {
		pn := &avsproto.TaskNode{Id: "cw1", Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE, TaskType: &avsproto.TaskNode_ContractWrite{
			ContractWrite: &avsproto.ContractWriteNode{Config: &avsproto.ContractWriteNode_Config{
				ContractAddress: addr,
				ChainId:         chainID,
			}},
		}}
		out, err := ProtoToOpenAPINode(pn)
		require.NoError(t, err)
		v, err := out.AsContractWriteNode()
		require.NoError(t, err)
		assert.Equal(t, chainID, int64(v.Config.ChainId))
	})

	// ethTransfer doubles as the corruption guard: Amount is a digit-only *string*
	// field. A blind unquote would turn "1000000000000000" into a number and break
	// it — the descriptor-driven approach must leave it a string while still
	// unquoting the int64 chainId sitting right next to it.
	t.Run("ethTransfer: chainId unquoted, Amount string preserved", func(t *testing.T) {
		pn := &avsproto.TaskNode{Id: "et1", Type: avsproto.NodeType_NODE_TYPE_ETH_TRANSFER, TaskType: &avsproto.TaskNode_EthTransfer{
			EthTransfer: &avsproto.ETHTransferNode{Config: &avsproto.ETHTransferNode_Config{
				Destination: addr,
				Amount:      "1000000000000000",
				ChainId:     chainID,
			}},
		}}
		out, err := ProtoToOpenAPINode(pn)
		require.NoError(t, err)
		v, err := out.AsETHTransferNode()
		require.NoError(t, err)
		assert.Equal(t, chainID, int64(v.Config.ChainId), "int64 chainId must survive the protojson string round-trip")
		assert.Equal(t, "1000000000000000", v.Config.Amount, "digit-only Amount string must NOT be unquoted to a number")
	})
}

// TestNodeRoundTrip_PerNodeChainId_FullCycle proves the complete create-render
// cycle a client sees: OpenAPI (chainId as a JSON number) → proto → OpenAPI,
// i.e. OpenAPIToProtoNode + ProtoToOpenAPINode chained — what CreateWorkflow then
// a subsequent GET exercise.
func TestNodeRoundTrip_PerNodeChainId_FullCycle(t *testing.T) {
	const chainID = int64(8453)
	typ := generated.ContractRead
	inner := generated.ContractReadNode{Type: &typ, Config: &generated.ContractReadNodeConfig{
		ContractAddress: generated.EthereumAddress("0x1234567890123456789012345678901234567890"),
		ChainId:         generated.ChainId(chainID),
	}}
	n := generated.Node{Id: "cr1", Type: generated.NodeTypeContractRead}
	require.NoError(t, n.FromContractReadNode(inner))

	pn, err := OpenAPIToProtoNode(n)
	require.NoError(t, err)
	require.Equal(t, chainID, pn.GetContractRead().GetConfig().GetChainId())

	rt, err := ProtoToOpenAPINode(pn)
	require.NoError(t, err)
	v, err := rt.AsContractReadNode()
	require.NoError(t, err)
	assert.Equal(t, chainID, int64(v.Config.ChainId))
}
