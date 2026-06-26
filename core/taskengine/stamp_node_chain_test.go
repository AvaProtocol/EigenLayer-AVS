package taskengine

import (
	"testing"

	"github.com/stretchr/testify/assert"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// TestStampNodeChainIfUnset covers the chain stamping used by RunNodeImmediately,
// including the Loop inner-runner path (regression: the runner was previously not
// stamped, so a Loop with a chain_id=0 contractWrite/Read/ethTransfer runner failed
// resolveSmartWalletForNode at execution).
func TestStampNodeChainIfUnset(t *testing.T) {
	const chainID = int64(11155111)

	t.Run("top-level chain-aware nodes get stamped", func(t *testing.T) {
		cw := &avsproto.TaskNode{TaskType: &avsproto.TaskNode_ContractWrite{
			ContractWrite: &avsproto.ContractWriteNode{Config: &avsproto.ContractWriteNode_Config{}},
		}}
		stampNodeChainIfUnset(cw, chainID)
		assert.Equal(t, chainID, cw.GetContractWrite().GetConfig().GetChainId())

		cr := &avsproto.TaskNode{TaskType: &avsproto.TaskNode_ContractRead{
			ContractRead: &avsproto.ContractReadNode{Config: &avsproto.ContractReadNode_Config{}},
		}}
		stampNodeChainIfUnset(cr, chainID)
		assert.Equal(t, chainID, cr.GetContractRead().GetConfig().GetChainId())

		et := &avsproto.TaskNode{TaskType: &avsproto.TaskNode_EthTransfer{
			EthTransfer: &avsproto.ETHTransferNode{Config: &avsproto.ETHTransferNode_Config{}},
		}}
		stampNodeChainIfUnset(et, chainID)
		assert.Equal(t, chainID, et.GetEthTransfer().GetConfig().GetChainId())
	})

	t.Run("loop inner runner gets stamped", func(t *testing.T) {
		loop := &avsproto.TaskNode{TaskType: &avsproto.TaskNode_Loop{
			Loop: &avsproto.LoopNode{Runner: &avsproto.LoopNode_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode{Config: &avsproto.ContractWriteNode_Config{}},
			}},
		}}
		stampNodeChainIfUnset(loop, chainID)
		assert.Equal(t, chainID, loop.GetLoop().GetContractWrite().GetConfig().GetChainId(),
			"loop runner chain_id should be stamped")
	})

	t.Run("already-set chain_id is not overwritten", func(t *testing.T) {
		const explicit = int64(8453)
		cw := &avsproto.TaskNode{TaskType: &avsproto.TaskNode_ContractWrite{
			ContractWrite: &avsproto.ContractWriteNode{Config: &avsproto.ContractWriteNode_Config{ChainId: explicit}},
		}}
		stampNodeChainIfUnset(cw, chainID)
		assert.Equal(t, explicit, cw.GetContractWrite().GetConfig().GetChainId())
	})

	t.Run("zero stamp chainID is a no-op", func(t *testing.T) {
		cw := &avsproto.TaskNode{TaskType: &avsproto.TaskNode_ContractWrite{
			ContractWrite: &avsproto.ContractWriteNode{Config: &avsproto.ContractWriteNode_Config{}},
		}}
		stampNodeChainIfUnset(cw, 0)
		assert.Equal(t, int64(0), cw.GetContractWrite().GetConfig().GetChainId())
	})
}
