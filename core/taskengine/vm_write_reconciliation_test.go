package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

func singlePendingWrite(t *testing.T, id string) *avsproto.Execution_Step {
	step := pendingConfirmationStep(t)
	methods := step.Metadata.AsInterface().([]interface{})
	step.Metadata, _ = structpb.NewValue(methods[:1])
	step.Id, step.Name = id, id
	step.ErrorCode = avsproto.ErrorCode_USER_OP_CONFIRMATION_UNRESOLVED
	step.Error = "UserOperation confirmation unresolved; check its status before retrying"
	step.OutputData = &avsproto.Execution_Step_ContractWrite{ContractWrite: &avsproto.ContractWriteNode_Output{}}
	return step
}

func reconciliationVM(t *testing.T, outcome string, polls *atomic.Int32) *VM {
	return confirmationTestVM(t, func(method string) any {
		if method == "eth_getUserOperationReceipt" {
			polls.Add(1)
			if outcome == "pending" {
				return nil
			}
			return map[string]any{"success": outcome == "confirmed", "receipt": map[string]any{"transactionHash": confirmationTestOtherHash, "status": "0x1"}}
		}
		if method == "eth_getTransactionReceipt" {
			return &types.Receipt{Status: 1, TxHash: common.HexToHash(confirmationTestOtherHash), BlockNumber: big.NewInt(10), Logs: []*types.Log{}}
		}
		return nil
	})
}

func TestWriteReconciliationResumeAfterAwait(t *testing.T) {
	for _, outcome := range []string{"confirmed", "reverted", "pending"} {
		t.Run(outcome, func(t *testing.T) {
			var polls atomic.Int32
			rpc := reconciliationVM(t, outcome, &polls)
			await := &avsproto.TaskNode{Id: "await", Name: "await", TaskType: &avsproto.TaskNode_Await{Await: &avsproto.AwaitNode{}}}
			report := customCodeNode("report")
			report.GetCustomCode().Config.Source = `return {data: supply.data, input: supply.input};`
			vm := dependencyVM(t, []*avsproto.TaskNode{dependencyWrite("approve"), await, dependencyWrite("supply"), report}, []*avsproto.TaskEdge{
				{Source: "trigger", Target: "approve"}, {Source: "approve", Target: "await"}, {Source: "await", Target: "supply"}, {Source: "supply", Target: "report"},
			})
			vm.smartWalletConfig = rpc.smartWalletConfig
			prefix := singlePendingWrite(t, "approve")
			vm.ExecutionLogs = []*avsproto.Execution_Step{prefix}
			vm.resumeCompleted = map[string]bool{"approve": true, "await": true}
			require.NoError(t, vm.Run())
			require.Same(t, prefix, vm.ExecutionLogs[0])
			require.EqualValues(t, 1, polls.Load(), "reconciliation is one receipt lookup, not polling")
			supply := dependencyStep(t, vm, "supply")
			if outcome == "confirmed" {
				require.True(t, prefix.Success)
				require.NotEqual(t, avsproto.ErrorCode_WRITE_PREREQUISITE_FAILED, supply.ErrorCode, "dependent write reaches its runner")
			} else {
				require.Equal(t, avsproto.ErrorCode_WRITE_PREREQUISITE_FAILED, supply.ErrorCode)
				require.True(t, dependencyStep(t, vm, "report").Success, "skipped data and input are readable without guards")
				require.Equal(t, map[string]interface{}{}, supply.GetContractWrite().GetData().AsInterface())
				if outcome == "reverted" {
					require.Equal(t, avsproto.ErrorCode_USER_OP_EXECUTION_REVERTED, prefix.ErrorCode)
				} else {
					require.Equal(t, avsproto.ErrorCode_USER_OP_CONFIRMATION_UNRESOLVED, prefix.ErrorCode)
				}
			}
		})
	}
}

func TestWriteReconciliationTimedOutLoopRetainsOutcome(t *testing.T) {
	for _, outcome := range []string{"confirmed", "reverted", "pending"} {
		t.Run(outcome, func(t *testing.T) {
			var polls atomic.Int32
			vm := reconciliationVM(t, outcome, &polls)
			pending := singlePendingWrite(t, "batch_iter_0")
			pending.ExecutionContext, _ = structpb.NewValue(map[string]interface{}{"chain_id": vm.smartWalletConfig.ChainID})
			vm.iterationWrites.Store(pending.Id, pending)
			snapshot := vm.timedOutIterationWrite(pending.Id)
			require.NotSame(t, pending, snapshot)
			require.Equal(t, avsproto.ErrorCode_USER_OP_CONFIRMATION_UNRESOLVED, snapshot.ErrorCode)
			data, _ := structpb.NewValue([]interface{}{nil})
			parent := &avsproto.Execution_Step{Id: "batch", OutputData: &avsproto.Execution_Step_Loop{Loop: &avsproto.LoopNode_Output{Data: data}}}
			propagateLoopConfirmationFailure(parent, []*avsproto.Execution_Step{snapshot})
			vm.reconcileWrite(parent)
			require.EqualValues(t, 1, polls.Load())
			require.Equal(t, outcome == "confirmed", parent.Success)
			if outcome == "reverted" {
				require.Equal(t, avsproto.ErrorCode_USER_OP_EXECUTION_REVERTED, parent.ErrorCode)
			}
			if outcome == "pending" {
				require.Equal(t, avsproto.ErrorCode_USER_OP_CONFIRMATION_UNRESOLVED, parent.ErrorCode)
			}
			require.Equal(t, "pending", extractUserOpHashStatus(pending), "collector never races by mutating the worker's step")
		})
	}
}

func extractUserOpHashStatus(step *avsproto.Execution_Step) string {
	return step.Metadata.AsInterface().([]interface{})[0].(map[string]interface{})["receipt"].(map[string]interface{})["status"].(string)
}

func TestWriteReconciliationPreservesOtherLoopFailures(t *testing.T) {
	var polls atomic.Int32
	vm := reconciliationVM(t, "confirmed", &polls)
	pending := singlePendingWrite(t, "batch_iter_0")
	data, _ := structpb.NewValue([]interface{}{nil, nil})
	parent := &avsproto.Execution_Step{Id: "batch", OutputData: &avsproto.Execution_Step_Loop{Loop: &avsproto.LoopNode_Output{Data: data}}}
	propagateLoopConfirmationFailure(parent, []*avsproto.Execution_Step{pending, {Id: "batch_iter_1", Success: false, Error: "unrelated failure"}})
	require.True(t, vm.reconcileWrite(parent))
	require.False(t, parent.Success, "resolving one pending op cannot erase another iteration's failure")
}

func TestWriteReconciliationUsesNodeChain(t *testing.T) {
	var polls atomic.Int32
	var log *types.Log
	vm := confirmationTestVM(t, func(method string) any {
		if method == "eth_getUserOperationReceipt" {
			polls.Add(1)
			return map[string]any{"success": false, "receipt": map[string]any{"transactionHash": confirmationTestOtherHash, "status": "0x1"}}
		}
		if method == "eth_getTransactionReceipt" {
			return &types.Receipt{Status: 1, TxHash: common.HexToHash(confirmationTestOtherHash), BlockNumber: big.NewInt(1), Logs: []*types.Log{log}}
		}
		return nil
	})
	resolved := *vm.smartWalletConfig
	resolved.ChainID = 8453
	log = confirmationRevertLog(t, resolved.EntryPointAddress(), confirmationTestHash, []byte{0xde, 0xad, 0xbe, 0xef})
	vm.smartWalletConfig = &config.SmartWalletConfig{ChainID: 11155111}
	vm.WithChainConfigResolver(func(chain int64) *config.SmartWalletConfig { require.EqualValues(t, 8453, chain); return &resolved })
	node := dependencyWrite("approve")
	node.GetContractWrite().Config.ChainId = 8453
	vm.TaskNodes = map[string]*avsproto.TaskNode{"approve": node}
	step := singlePendingWrite(t, "approve")
	require.True(t, vm.reconcileWrite(step))
	require.Contains(t, step.Error, "0xdeadbeef")
	receipt := step.Metadata.AsInterface().([]interface{})[0].(map[string]interface{})["receipt"].(map[string]interface{})
	require.Equal(t, "0x1", receipt["status"], "status describes the outer transaction")
	require.Equal(t, "failed", receipt["executionStatus"])
}

func TestWriteReconciliationDeadline(t *testing.T) {
	// One-shot lookup obeys its caller's deadline even if the RPC never replies.
	vm := confirmationTestVM(t, func(string) any { time.Sleep(50 * time.Millisecond); return nil })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := vm.lookupUserOpConfirmation(ctx, confirmationTestHash, vm.smartWalletConfig)
	require.Error(t, err, fmt.Sprintf("lookup took %s", time.Since(started)))
	require.Less(t, time.Since(started), time.Second)
}

func TestWriteReconciliationNativeTransfer(t *testing.T) {
	for _, outcome := range []string{"confirmed", "reverted", "pending"} {
		t.Run(outcome, func(t *testing.T) {
			var polls atomic.Int32
			vm := reconciliationVM(t, outcome, &polls)
			metadata, _ := structpb.NewValue(map[string]interface{}{"receiptStatus": "pending", "userOpHash": confirmationTestHash})
			step := &avsproto.Execution_Step{Id: "native", Metadata: metadata, ErrorCode: avsproto.ErrorCode_USER_OP_CONFIRMATION_UNRESOLVED, OutputData: &avsproto.Execution_Step_EthTransfer{EthTransfer: &avsproto.ETHTransferNode_Output{}}}
			vm.reconcileWrite(step)
			require.EqualValues(t, 1, polls.Load())
			require.Equal(t, outcome == "confirmed", step.Success)
			if outcome == "pending" {
				require.Equal(t, avsproto.ErrorCode_USER_OP_CONFIRMATION_UNRESOLVED, step.ErrorCode)
			}
			if outcome == "reverted" {
				require.Equal(t, avsproto.ErrorCode_USER_OP_EXECUTION_REVERTED, step.ErrorCode)
			}
		})
	}
}

func TestWriteReconciliationKeepsPendingSibling(t *testing.T) {
	var polls atomic.Int32
	vm := confirmationTestVM(t, func(method string) any {
		if method == "eth_getUserOperationReceipt" && polls.Add(1) == 1 {
			return map[string]any{"success": true, "receipt": map[string]any{"transactionHash": confirmationTestOtherHash, "status": "0x1"}}
		}
		return nil
	})
	step := pendingConfirmationStep(t)
	require.True(t, vm.reconcileWrite(step))
	require.False(t, step.Success)
	require.Equal(t, avsproto.ErrorCode_USER_OP_CONFIRMATION_UNRESOLVED, step.ErrorCode)
	require.Equal(t, confirmationTestOtherHash, extractUserOpHashFromStep(step))
}

func TestNodeExecutionContextUsesRunnerChain(t *testing.T) {
	vm := NewVM()
	vm.smartWalletConfig = &config.SmartWalletConfig{ChainID: 11155111}
	for _, native := range []bool{false, true} {
		node := dependencyWrite("write")
		node.GetContractWrite().Config.ChainId = 8453
		if native {
			node = &avsproto.TaskNode{Id: "native", Type: avsproto.NodeType_NODE_TYPE_ETH_TRANSFER, TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: &avsproto.ETHTransferNode{Config: &avsproto.ETHTransferNode_Config{ChainId: 8453}}}}
		}
		step := CreateNodeExecutionStep(node.Id, node, vm)
		require.EqualValues(t, 8453, step.ExecutionContext.AsInterface().(map[string]interface{})["chain_id"])
	}
}

func TestLoopConfirmationUsesParentRunnerOverOldCheckpointContext(t *testing.T) {
	vm := NewVM()
	resolved := &config.SmartWalletConfig{ChainID: 8453}
	vm.smartWalletConfig = &config.SmartWalletConfig{ChainID: 11155111}
	vm.WithChainConfigResolver(func(chain int64) *config.SmartWalletConfig { require.EqualValues(t, 8453, chain); return resolved })
	vm.TaskNodes = map[string]*avsproto.TaskNode{"batch": {Id: "batch", TaskType: &avsproto.TaskNode_Loop{Loop: &avsproto.LoopNode{Runner: &avsproto.LoopNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{Config: &avsproto.ContractWriteNode_Config{ChainId: 8453}}}}}}}
	step := singlePendingWrite(t, "batch_iter_0")
	step.ExecutionContext, _ = structpb.NewValue(map[string]interface{}{"chain_id": 11155111})
	got, err := vm.confirmationConfig(step)
	require.NoError(t, err)
	require.Same(t, resolved, got)
}

func TestWriteReconciliationSharedPrerequisiteOnResumedFanout(t *testing.T) {
	for _, loop := range []bool{false, true} {
		for _, outcome := range []string{"confirmed", "reverted", "pending"} {
			t.Run(fmt.Sprintf("loop=%t/%s", loop, outcome), func(t *testing.T) {
				lookupStarted, releaseLookup := make(chan struct{}), make(chan struct{})
				extraLookup := make(chan struct{}, 8)
				var polls atomic.Int32
				rpc := confirmationTestVM(t, func(method string) any {
					switch method {
					case "eth_getUserOperationReceipt":
						if polls.Add(1) != 1 {
							extraLookup <- struct{}{}
							return nil // A lagging replica must not split the successors' outcome.
						}
						close(lookupStarted)
						<-releaseLookup
						if outcome == "pending" {
							return nil
						}
						return map[string]any{"success": outcome == "confirmed", "receipt": map[string]any{"transactionHash": confirmationTestOtherHash, "status": "0x1"}}
					case "eth_getTransactionReceipt":
						return &types.Receipt{Status: 1, TxHash: common.HexToHash(confirmationTestOtherHash), BlockNumber: big.NewInt(10), Logs: []*types.Log{}}
					}
					return nil
				})
				var release sync.Once
				t.Cleanup(func() { release.Do(func() { close(releaseLookup) }) })
				ancestor := dependencyWrite("approve")
				prefix := singlePendingWrite(t, "approve")
				if loop {
					ancestor = &avsproto.TaskNode{Id: "approve", Name: "approve", TaskType: &avsproto.TaskNode_Loop{Loop: &avsproto.LoopNode{
						Runner: &avsproto.LoopNode_ContractWrite{ContractWrite: dependencyWrite("write").GetContractWrite()},
					}}}
					data, _ := structpb.NewValue([]interface{}{nil})
					prefix = &avsproto.Execution_Step{Id: "approve", Name: "approve", OutputData: &avsproto.Execution_Step_Loop{Loop: &avsproto.LoopNode_Output{Data: data}}}
					propagateLoopConfirmationFailure(prefix, []*avsproto.Execution_Step{singlePendingWrite(t, "approve_iter_0")})
				}
				await := &avsproto.TaskNode{Id: "await", Name: "await", TaskType: &avsproto.TaskNode_Await{Await: &avsproto.AwaitNode{}}}
				report := customCodeNode("report")
				if loop && outcome == "confirmed" {
					report.GetCustomCode().Config.Source = `return {hash: approve.data[0].metadata.transactionHash};`
				}
				vm := dependencyVM(t, []*avsproto.TaskNode{ancestor, await, dependencyWrite("supply"), dependencyWrite("repay"), report}, []*avsproto.TaskEdge{
					{Source: "trigger", Target: "approve"}, {Source: "approve", Target: "await"},
					{Source: "await", Target: "supply"}, {Source: "await", Target: "repay"},
					{Source: "supply", Target: "report"}, {Source: "repay", Target: "report"},
				})
				vm.smartWalletConfig = rpc.smartWalletConfig
				vm.ExecutionLogs = []*avsproto.Execution_Step{prefix}
				vm.resumeCompleted = map[string]bool{"approve": true, "await": true}
				done := make(chan error, 1)
				go func() { done <- vm.Run() }()
				select {
				case <-lookupStarted:
				case <-time.After(2 * time.Second):
					t.Fatal("receipt lookup did not start")
				}
				select {
				case <-extraLookup:
					t.Error("parallel successors started separate receipt lookups")
				case <-time.After(100 * time.Millisecond):
				}
				started := time.Now()
				release.Do(func() { close(releaseLookup) })
				select {
				case err := <-done:
					require.NoError(t, err)
				case <-time.After(8 * time.Second):
					t.Fatal("fan-out did not finish")
				}
				require.EqualValues(t, 1, polls.Load())
				require.Same(t, prefix, vm.ExecutionLogs[0])
				require.True(t, dependencyStep(t, vm, "report").Success)
				if loop && outcome == "confirmed" {
					require.Equal(t, confirmationTestOtherHash, dependencyStep(t, vm, "report").GetCustomCode().Data.AsInterface().(map[string]interface{})["hash"])
				}
				for _, id := range []string{"supply", "repay"} {
					if outcome == "confirmed" {
						require.NotEqual(t, avsproto.ErrorCode_WRITE_PREREQUISITE_FAILED, dependencyStep(t, vm, id).ErrorCode)
					} else {
						require.Equal(t, avsproto.ErrorCode_WRITE_PREREQUISITE_FAILED, dependencyStep(t, vm, id).ErrorCode)
					}
				}
				if outcome == "confirmed" {
					require.True(t, prefix.Success)
					require.GreaterOrEqual(t, time.Since(started), 5*time.Second, "successors wait for propagation")
				} else {
					require.Less(t, time.Since(started), 2*time.Second, "no propagation delay for unresolved/reverted receipts")
				}
			})
		}
	}
}

func TestWriteReconciliationPreservesNewerCanonicalReceipt(t *testing.T) {
	for _, partial := range []bool{false, true} {
		for _, confirmed := range []bool{false, true} {
			t.Run(fmt.Sprintf("partial=%t/confirmed=%t", partial, confirmed), func(t *testing.T) {
				lookupStarted, releaseLookup := make(chan struct{}), make(chan struct{})
				var polls atomic.Int32
				rpc := confirmationTestVM(t, func(method string) any {
					switch method {
					case "eth_getUserOperationReceipt":
						if polls.Add(1) == 1 && partial {
							return map[string]any{"success": true, "receipt": map[string]any{"transactionHash": confirmationTestOtherHash, "status": "0x1"}}
						}
						close(lookupStarted)
						<-releaseLookup
					case "eth_getTransactionReceipt":
						return &types.Receipt{Status: 1, TxHash: common.HexToHash(confirmationTestOtherHash), BlockNumber: big.NewInt(10), Logs: []*types.Log{}}
					}
					return nil
				})
				var release sync.Once
				t.Cleanup(func() { release.Do(func() { close(releaseLookup) }) })
				vm := dependencyVM(t, []*avsproto.TaskNode{dependencyWrite("approve"), dependencyWrite("supply")}, []*avsproto.TaskEdge{
					{Source: "trigger", Target: "approve"}, {Source: "approve", Target: "supply"},
				})
				vm.smartWalletConfig = rpc.smartWalletConfig
				prefix := singlePendingWrite(t, "approve")
				if partial {
					prefix = pendingConfirmationStep(t)
					prefix.Id, prefix.Name = "approve", "approve"
					prefix.OutputData = &avsproto.Execution_Step_ContractWrite{ContractWrite: &avsproto.ContractWriteNode_Output{}}
				}
				vm.ExecutionLogs = []*avsproto.Execution_Step{prefix}
				done := make(chan *avsproto.Execution_Step, 1)
				go func() { done <- vm.reconciledWritePrerequisite("supply") }()
				select {
				case <-lookupStarted:
				case <-time.After(2 * time.Second):
					t.Fatal("receipt lookup did not start")
				}
				if partial {
					vm.applyConfirmedWrite(prefix, confirmationTestHash, &waitForUserOpConfirmationResult{Success: true, TxHash: confirmationTestOtherHash})
				}
				hash := confirmationTestHash
				if partial {
					hash = confirmationTestOtherHash
				}
				vm.applyConfirmedWrite(prefix, hash, &waitForUserOpConfirmationResult{Success: confirmed, TxHash: confirmationTestOtherHash})
				newer := proto.Clone(prefix).(*avsproto.Execution_Step)
				release.Do(func() { close(releaseLookup) })
				select {
				case blocked := <-done:
					if confirmed {
						require.Nil(t, blocked)
					} else {
						require.Equal(t, avsproto.ErrorCode_USER_OP_EXECUTION_REVERTED, blocked.GetErrorCode())
					}
				case <-time.After(2 * time.Second):
					t.Fatal("receipt lookup did not finish")
				}
				require.True(t, proto.Equal(newer, prefix), "neither failed nor partial lookups may overwrite a newer receipt")
			})
		}
	}
}

func TestWriteReconciliationAlreadyConfirmedDoesNotWait(t *testing.T) {
	var polls atomic.Int32
	rpc := reconciliationVM(t, "pending", &polls)
	vm := dependencyVM(t, []*avsproto.TaskNode{dependencyWrite("approve"), dependencyWrite("supply")}, []*avsproto.TaskEdge{
		{Source: "trigger", Target: "approve"}, {Source: "approve", Target: "supply"},
	})
	vm.smartWalletConfig = rpc.smartWalletConfig
	vm.ExecutionLogs = []*avsproto.Execution_Step{{Id: "approve", Success: true}}
	started := time.Now()
	require.Nil(t, vm.reconciledWritePrerequisite("supply"))
	require.Zero(t, polls.Load())
	require.Less(t, time.Since(started), time.Second)
}

func TestLoopCollectorTimeoutUsesConfirmedReceipt(t *testing.T) {
	for _, outcome := range []string{"confirmed", "reverted", "pending", "not-submitted"} {
		t.Run(outcome, func(t *testing.T) {
			var polls atomic.Int32
			vm := reconciliationVM(t, "pending", &polls)
			step := singlePendingWrite(t, "batch_iter_0")
			data, _ := structpb.NewValue(map[string]interface{}{"amount": "1"})
			step.GetContractWrite().Data = data
			step.GasUsed, step.TotalGasCost = "21000", "42"
			if outcome == "confirmed" || outcome == "reverted" {
				vm.applyConfirmedWrite(step, confirmationTestHash, &waitForUserOpConfirmationResult{Success: outcome == "confirmed", TxHash: confirmationTestOtherHash})
			}
			if outcome != "not-submitted" {
				vm.iterationWrites.Store(step.Id, step)
			}
			// No result is sent: force the timer arm used by both collectors.
			result, timedOut := vm.waitForLoopIterationResult(step.Id, make(chan *ExecutionResult), time.Millisecond)
			require.Equal(t, outcome != "confirmed", timedOut)
			if outcome == "not-submitted" {
				require.Nil(t, result.Step)
				return
			}
			require.NotSame(t, step, result.Step)
			require.Equal(t, step.GasUsed, result.Step.GasUsed)
			require.Equal(t, step.TotalGasCost, result.Step.TotalGasCost)
			if outcome == "confirmed" {
				require.True(t, result.Step.Success)
				require.Equal(t, "1", result.Data.(map[string]interface{})["amount"])
				require.Equal(t, confirmationTestOtherHash, result.Data.(map[string]interface{})["metadata"].(map[string]interface{})["transactionHash"])
			} else {
				require.Nil(t, result.Data)
				code := avsproto.ErrorCode_USER_OP_CONFIRMATION_UNRESOLVED
				if outcome == "reverted" {
					code = avsproto.ErrorCode_USER_OP_EXECUTION_REVERTED
				}
				require.Equal(t, code, result.Step.ErrorCode)
			}
		})
	}
}
