package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

const prerequisiteReceiptTimeout = 5 * time.Second

func workflowWriteChainID(node *avsproto.TaskNode) int64 {
	if node == nil {
		return 0
	}
	if write := node.GetContractWrite(); write != nil {
		return write.Config.GetChainId()
	}
	if native := node.GetEthTransfer(); native != nil {
		return native.Config.GetChainId()
	}
	if loop := node.GetLoop(); loop != nil {
		if write := loop.GetContractWrite(); write != nil {
			return write.Config.GetChainId()
		}
		if native := loop.GetEthTransfer(); native != nil {
			return native.Config.GetChainId()
		}
	}
	return 0
}

func (v *VM) confirmationConfig(step *avsproto.Execution_Step) (*config.SmartWalletConfig, error) {
	// The graph is authoritative. Iterations are ephemeral nodes, so their
	// execution context records the resolved runner chain for a later resume.
	v.mu.Lock()
	chain := workflowWriteChainID(v.TaskNodes[step.Id])
	v.mu.Unlock()
	if chain > 0 {
		return v.resolveSmartWalletForNode(chain)
	}
	if step.ExecutionContext != nil {
		if values, ok := step.ExecutionContext.AsInterface().(map[string]interface{}); ok {
			if chain, ok := values["chain_id"].(float64); ok && chain > 0 {
				return v.resolveSmartWalletForNode(int64(chain))
			}
		}
	}
	// Older checkpoints lacked a per-node context. Only the single-chain
	// configuration is safe to inherit; never guess a chain in gateway mode.
	if v.chainConfigResolver != nil {
		return nil, fmt.Errorf("confirmation chain unavailable for step %s", step.Id)
	}
	return v.smartWalletConfig, nil
}

func (v *VM) waitForStepUserOpConfirmation(step *avsproto.Execution_Step, hash string) (*waitForUserOpConfirmationResult, error) {
	sw, err := v.confirmationConfig(step)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(v.context()), userOpConfirmationTimeout)
	defer cancel()
	return v.waitForUserOpConfirmationOnChain(ctx, hash, sw)
}

// Recheck each unresolved operation once, with a single deadline for the whole
// prerequisite. This is reconciliation of an already submitted operation,
// never resubmission. An RPC failure is still an unknown outcome.
func (v *VM) reconcileWrite(step *avsproto.Execution_Step) bool {
	if v.IsSimulation || step == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(v.context()), prerequisiteReceiptTimeout)
	defer cancel()
	return v.reconcileWriteContext(ctx, step)
}

func (v *VM) reconcileWriteContext(ctx context.Context, step *avsproto.Execution_Step) bool {
	if step.GetLoop() != nil {
		return v.reconcileLoopWrite(ctx, step)
	}
	sw, err := v.confirmationConfig(step)
	if err != nil {
		return false
	}

	changed := false
	for {
		hash := extractUserOpHashFromStep(step)
		if hash == "" && pendingETHTransfer(step) {
			hash, _ = step.Metadata.AsInterface().(map[string]interface{})["userOpHash"].(string)
		}
		if hash == "" {
			break
		}
		result, err := v.lookupUserOpConfirmation(ctx, hash, sw)
		if err != nil || result == nil {
			break
		}
		v.applyConfirmedWrite(step, hash, result)
		changed = true
	}
	if shouldWaitForContractWriteConfirmation(step, false) || pendingETHTransfer(step) {
		v.markUserOpConfirmationUnresolved(step)
	}
	return changed
}

func (v *VM) applyConfirmedWrite(step *avsproto.Execution_Step, hash string, result *waitForUserOpConfirmationResult) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if shouldWaitForContractWriteConfirmation(step, false) {
		updateConfirmedWriteMetadata(step, hash, result)
	} else if pendingETHTransfer(step) {
		metadata := step.Metadata.AsInterface().(map[string]interface{})
		step.Success, step.Error, step.ErrorCode = result.Success, "", avsproto.ErrorCode_ERROR_CODE_UNSPECIFIED
		metadata["transactionHash"], metadata["receiptStatus"] = result.TxHash, "confirmed"
		if !result.Success {
			step.Error, step.ErrorCode = confirmedFailureReason(result), avsproto.ErrorCode_USER_OP_EXECUTION_REVERTED
			metadata["receiptStatus"] = "failed"
			if result.RevertData != "" {
				metadata["revertData"] = result.RevertData
			}
		}
		step.Metadata, _ = structpb.NewValue(metadata)
		step.EndAt = time.Now().UnixMilli()
	}
	if result.Receipt != nil && (step.TotalGasCost == "" || step.TotalGasCost == "0") {
		updateStepGasCostFromReceipt(step, result.Receipt, v)
	}
}

func (v *VM) reconciledWritePrerequisite(nodeID string) *avsproto.Execution_Step {
	checked := map[string]bool{}
	for {
		prior := v.failedWritePrerequisite(nodeID)
		if prior == nil || checked[prior.Id] || v.IsSimulation {
			return prior
		}
		checked[prior.Id] = true
		if !v.reconcileWrite(prior) {
			return prior
		}
		// Replace fields on the original step, not its identity; reports and
		// checkpointing retain its receipts and no duplicate execution is appended.
		v.mu.Lock()
		for _, original := range v.ExecutionLogs {
			if original.Id == prior.Id {
				proto.Reset(original)
				proto.Merge(original, prior)
				break
			}
		}
		v.mu.Unlock()
		if prior.GetLoop() != nil && prior.GetLoop().Data != nil {
			(&CommonProcessor{vm: v}).SetOutputVarForStep(prior.Id, prior.GetLoop().Data.AsInterface())
		}
		if !prior.Success {
			return prior
		}
	}
}

// The collector may time out while the worker is waiting for a receipt. Copy
// the already-submitted step under the same lock as the waiter and keep its
// hash on the parent loop instead of losing it to a generic collector error.
func (v *VM) timedOutIterationWrite(id string) *avsproto.Execution_Step {
	raw, ok := v.iterationWrites.Load(id)
	if !ok {
		return nil
	}
	v.mu.Lock()
	step := proto.Clone(raw.(*avsproto.Execution_Step)).(*avsproto.Execution_Step)
	v.mu.Unlock()
	if shouldWaitForContractWriteConfirmation(step, false) || pendingETHTransfer(step) {
		v.markUserOpConfirmationUnresolved(step)
	}
	return step
}

func (v *VM) reconcileLoopWrite(ctx context.Context, parent *avsproto.Execution_Step) bool {
	if parent.Metadata == nil {
		return false
	}
	meta, ok := parent.Metadata.AsInterface().(map[string]interface{})
	if !ok {
		return false
	}
	outcomes, _ := meta["confirmationOutcomes"].([]interface{})
	changed, allSuccess := false, true
	var selected *avsproto.Execution_Step
	var results []interface{}
	if data := parent.GetLoop().GetData(); data != nil {
		results, _ = data.AsInterface().([]interface{})
	}
	for _, raw := range outcomes {
		outcome, ok := raw.(map[string]interface{})
		if !ok {
			allSuccess = false
			continue
		}
		encoded, err := json.Marshal(outcome["step"])
		iteration := new(avsproto.Execution_Step)
		if err != nil || protojson.Unmarshal(encoded, iteration) != nil {
			allSuccess = false
			continue
		}
		if v.reconcileWriteContext(ctx, iteration) {
			changed = true
		}
		outcome["error"], outcome["errorCode"] = iteration.Error, iteration.ErrorCode.String()
		outcome["metadata"] = iteration.Metadata.AsInterface()
		outcome["step"] = confirmationStepSnapshot(iteration)
		if !iteration.Success {
			allSuccess = false
			if selected == nil || iteration.ErrorCode == avsproto.ErrorCode_USER_OP_CONFIRMATION_UNRESOLVED {
				selected = iteration
			}
		} else if index, err := strconv.Atoi(strings.TrimPrefix(iteration.Id, parent.Id+"_iter_")); err == nil && index >= 0 && index < len(results) {
			results[index] = (&ExecutionQueue{}).extractResultData(iteration)
		}
	}
	if !changed {
		return false
	}
	if selected != nil {
		parent.Error = fmt.Sprintf("Loop iteration %s: %s", selected.Id, selected.Error)
		parent.ErrorCode = selected.ErrorCode
	}
	if allSuccess && meta["confirmationOnlyFailure"] != true {
		parent.Error = "One or more loop iterations failed"
		parent.ErrorCode = avsproto.ErrorCode_INVALID_REQUEST
	}
	if allSuccess && meta["confirmationOnlyFailure"] == true {
		parent.Success, parent.Error, parent.ErrorCode = true, "", avsproto.ErrorCode_ERROR_CODE_UNSPECIFIED
	}
	if results != nil {
		parent.GetLoop().Data, _ = structpb.NewValue(results)
	}
	parent.Metadata, _ = structpb.NewValue(meta)
	return true
}

func confirmationStepSnapshot(step *avsproto.Execution_Step) interface{} {
	encoded, _ := protojson.Marshal(step)
	var snapshot interface{}
	_ = json.Unmarshal(encoded, &snapshot)
	return snapshot
}
