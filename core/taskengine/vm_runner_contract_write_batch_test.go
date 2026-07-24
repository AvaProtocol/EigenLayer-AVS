package taskengine

import (
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// approveCalldataHex builds a valid ERC-20 approve(spender, amount) calldata (non-zero spender so
// it clears the real-path pre-flight validation).
func approveCalldataHex(spender common.Address, amount int64) string {
	sel := "095ea7b3"
	sp := common.Bytes2Hex(common.LeftPadBytes(spender.Bytes(), 32))
	amt := common.Bytes2Hex(common.LeftPadBytes(big.NewInt(amount).Bytes(), 32))
	return "0x" + sel + sp + amt
}

// stepResults pulls the per-method result array (the `metadata.results[]` shape surfaced to clients)
// out of an execution step.
func stepResults(t *testing.T, step *avsproto.Execution_Step) []map[string]any {
	t.Helper()
	require.NotNil(t, step.Metadata)
	arr, ok := step.Metadata.AsInterface().([]interface{})
	require.True(t, ok, "step metadata should be an array of method results")
	out := make([]map[string]any, len(arr))
	for i, e := range arr {
		m, ok := e.(map[string]interface{})
		require.True(t, ok)
		out[i] = m
	}
	return out
}

// G4: on-demand (nodes:run) real execution of a multi-call node — the approve+swap shape — must
// submit ONE atomic UserOp (executeBatch) across the per-call targets instead of one UserOp per
// method. Deployed workflows must keep the existing sequential per-method path unchanged.
func TestAtomicBatchOnDemand(t *testing.T) {
	owner := common.HexToAddress("0x1111111111111111111111111111111111111111")
	runner := common.HexToAddress("0x2222222222222222222222222222222222222222")
	tokenA := common.HexToAddress("0xaAaa000000000000000000000000000000000001")
	tokenB := common.HexToAddress("0xbBbb000000000000000000000000000000000002")
	router := common.HexToAddress("0xcCcc000000000000000000000000000000000003")
	swConfig := ondemandTestConfig()

	falsePtr := false

	// A node with two method calls on two DIFFERENT contracts via per-call contract_address:
	// approve(router)@tokenA + approve(router)@tokenB. Pre-encoded calldata, so no ABI is needed.
	makeNode := func() *avsproto.ContractWriteNode {
		cdA := approveCalldataHex(router, 1000)
		cdB := approveCalldataHex(router, 2000)
		addrA := tokenA.Hex()
		addrB := tokenB.Hex()
		return &avsproto.ContractWriteNode{
			Config: &avsproto.ContractWriteNode_Config{
				ContractAddress: tokenA.Hex(), // node-level default target
				ChainId:         swConfig.ChainID,
				IsSimulated:     &falsePtr, // force the real execution path
				MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
					{MethodName: "approve", CallData: &cdA, ContractAddress: &addrA},
					{MethodName: "approve", CallData: &cdB, ContractAddress: &addrB},
				},
			},
		}
	}

	countingSender := func(callCount *int, captured *[]byte, status uint64) SendUserOpFunc {
		return func(_ *config.SmartWalletConfig, _ common.Address, callData []byte, _ *preset.VerifyingPaymasterRequest, _ *common.Address, _ *big.Int, _ *big.Int, _ logger.Logger) (*userop.UserOperation, *types.Receipt, error) {
			*callCount++
			if captured != nil {
				*captured = callData
			}
			return wellFormedUserOp(runner), minedReceipt(status), nil
		}
	}

	t.Run("nodes:run multi-call submits ONE atomic UserOp with both targets", func(t *testing.T) {
		vm, err := NewVMWithData(nil, nil, swConfig, nil) // task == nil => on-demand route
		require.NoError(t, err)
		vm.AddVar("aa_sender", runner.Hex())
		processor := NewContractWriteProcessor(vm, nil, swConfig, owner)

		var callCount int
		var captured []byte
		processor.sendUserOpFunc = countingSender(&callCount, &captured, 1)

		step, err := processor.Execute("step1", makeNode())
		require.NoError(t, err)
		require.NotNil(t, step)

		// The whole approve+approve set went out as ONE UserOp, not two.
		require.Equal(t, 1, callCount, "atomic batch must submit exactly one UserOp")

		// And that UserOp's calldata is an executeBatch of both calls, on their per-call targets.
		targets, values, datas, err := aa.UnpackExecuteCalldata(captured)
		require.NoError(t, err)
		require.Len(t, targets, 2)
		assert.Equal(t, tokenA, targets[0])
		assert.Equal(t, tokenB, targets[1])
		assert.Equal(t, int64(0), values[0].Int64())
		assert.Equal(t, int64(0), values[1].Int64())
		assert.GreaterOrEqual(t, len(datas[0]), 4)
		assert.GreaterOrEqual(t, len(datas[1]), 4)

		// Two per-method results, both confirmed, sharing one tx identity.
		results := stepResults(t, step)
		require.Len(t, results, 2)
		receipt0, ok0 := results[0]["receipt"].(map[string]interface{})
		receipt1, ok1 := results[1]["receipt"].(map[string]interface{})
		require.True(t, ok0)
		require.True(t, ok1)
		assert.Equal(t, receipt0["transactionHash"], receipt1["transactionHash"], "both sub-calls share the batch tx hash")
		assert.Equal(t, true, results[0]["success"])
		assert.Equal(t, true, results[1]["success"])
		assert.True(t, step.Success)

		// The two results share one on-chain receipt (gasUsed 21000), so step gas must be counted
		// once — not summed per sub-call. Guards against the fan-out double-counting gas.
		assert.Equal(t, "21000", step.GasUsed, "batch gas must be counted once across sub-calls, not 2x")
	})

	t.Run("a reverted batch fails all sub-calls atomically", func(t *testing.T) {
		vm, err := NewVMWithData(nil, nil, swConfig, nil)
		require.NoError(t, err)
		vm.AddVar("aa_sender", runner.Hex())
		processor := NewContractWriteProcessor(vm, nil, swConfig, owner)

		var callCount int
		processor.sendUserOpFunc = countingSender(&callCount, nil, 0) // status 0 = on-chain revert

		step, err := processor.Execute("step1", makeNode())
		require.NoError(t, err)
		require.Equal(t, 1, callCount, "still exactly one UserOp")

		results := stepResults(t, step)
		require.Len(t, results, 2)
		assert.Equal(t, false, results[0]["success"])
		assert.Equal(t, false, results[1]["success"])
		assert.False(t, step.Success, "a reverted batch is not a partial success")
	})

	t.Run("deployed workflow keeps the sequential per-method path", func(t *testing.T) {
		task := &model.Workflow{Task: &avsproto.Task{
			Id:      "deployed-1",
			Owner:   owner.Hex(),
			Trigger: &avsproto.TaskTrigger{Id: "trg", Name: "trg", Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL, TriggerType: &avsproto.TaskTrigger_Manual{Manual: &avsproto.ManualTrigger{}}},
		}}
		vm, err := NewVMWithData(task, nil, swConfig, nil) // task != nil => NOT the on-demand route
		require.NoError(t, err)
		vm.AddVar("aa_sender", runner.Hex())
		processor := NewContractWriteProcessor(vm, nil, swConfig, owner)

		var callCount int
		processor.sendUserOpFunc = countingSender(&callCount, nil, 1)

		_, err = processor.Execute("step1", makeNode())
		require.NoError(t, err)
		// Two method calls on a deployed task => two sequential UserOps (unchanged, non-batched).
		require.Equal(t, 2, callCount, "deployed multi-call path must remain one UserOp per method")
	})

	t.Run("a value-bearing call packs executeBatchWithValues", func(t *testing.T) {
		vm, err := NewVMWithData(nil, nil, swConfig, nil)
		require.NoError(t, err)
		vm.AddVar("aa_sender", runner.Hex())
		processor := NewContractWriteProcessor(vm, nil, swConfig, owner)

		var callCount int
		var captured []byte
		processor.sendUserOpFunc = countingSender(&callCount, &captured, 1)

		// Node-level value applies to the LAST sub-call, so the batch carries a non-zero value and
		// must fall back from executeBatch to executeBatchWithValues.
		cdA := approveCalldataHex(router, 1000)
		cdB := approveCalldataHex(router, 2000)
		addrA := tokenA.Hex()
		addrB := tokenB.Hex()
		value := "12345"
		node := &avsproto.ContractWriteNode{
			Config: &avsproto.ContractWriteNode_Config{
				ContractAddress: tokenA.Hex(),
				ChainId:         swConfig.ChainID,
				IsSimulated:     &falsePtr,
				Value:           &value,
				MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
					{MethodName: "approve", CallData: &cdA, ContractAddress: &addrA},
					{MethodName: "approve", CallData: &cdB, ContractAddress: &addrB},
				},
			},
		}

		_, err = processor.Execute("step1", node)
		require.NoError(t, err)
		require.Equal(t, 1, callCount, "still one UserOp")

		require.GreaterOrEqual(t, len(captured), 4)
		assert.Equal(t, "c3ff72fc", common.Bytes2Hex(captured[:4]), "value-bearing batch must use executeBatchWithValues")
		_, values, _, err := aa.UnpackExecuteCalldata(captured)
		require.NoError(t, err)
		require.Len(t, values, 2)
		assert.Equal(t, int64(0), values[0].Int64(), "approve (not last) carries 0 value")
		assert.Equal(t, "12345", values[1].String(), "node value lands on the last sub-call")
	})

	t.Run("an unresolvable per-call contractAddress fails fast (no UserOp)", func(t *testing.T) {
		vm, err := NewVMWithData(nil, nil, swConfig, nil)
		require.NoError(t, err)
		vm.AddVar("aa_sender", runner.Hex())
		processor := NewContractWriteProcessor(vm, nil, swConfig, owner)

		var callCount int
		processor.sendUserOpFunc = countingSender(&callCount, nil, 1)

		cdA := approveCalldataHex(router, 1000)
		cdB := approveCalldataHex(router, 2000)
		addrA := tokenA.Hex()
		bad := "0xNOT_A_VALID_ADDRESS"
		node := &avsproto.ContractWriteNode{
			Config: &avsproto.ContractWriteNode_Config{
				ContractAddress: tokenA.Hex(),
				ChainId:         swConfig.ChainID,
				IsSimulated:     &falsePtr,
				MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
					{MethodName: "approve", CallData: &cdA, ContractAddress: &addrA},
					{MethodName: "approve", CallData: &cdB, ContractAddress: &bad},
				},
			},
		}

		_, err = processor.Execute("step1", node)
		require.Error(t, err, "an invalid per-call target must fail the node, not silently fall back")
		require.Equal(t, 0, callCount, "no UserOp should be sent when a target is invalid")
	})
}
