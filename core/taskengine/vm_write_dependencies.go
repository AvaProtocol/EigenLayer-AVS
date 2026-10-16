package taskengine

import (
	"fmt"
	"strings"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
)

// isWorkflowWrite includes loop runners: a failed transfer in a loop is also
// a failed prerequisite of writes downstream of that loop. Loop iterations
// themselves keep their existing sequencing; graph edges define dependencies.
func isWorkflowWrite(node *avsproto.TaskNode) bool {
	if node == nil {
		return false
	}
	if node.GetContractWrite() != nil || node.GetEthTransfer() != nil {
		return true
	}
	loop := node.GetLoop()
	return loop != nil && (loop.GetContractWrite() != nil || loop.GetEthTransfer() != nil)
}

// failedWritePrerequisite walks graph ancestry, rather than just the immediate
// parent: a read or code node between approve and supply cannot erase a failed
// approval. Only completed writes with a failed/unresolved outcome block a
// write. Unselected branch paths and independent siblings have no effect.
// Return a copy because confirmation metadata can be updated under the VM lock.
func (v *VM) failedWritePrerequisite(nodeID string) *avsproto.Execution_Step {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.task == nil || v.task.Task == nil {
		return nil
	}
	parents := make(map[string][]string)
	for _, edge := range v.task.Edges {
		// A branch condition is a conceptual output of its real branch node.
		source := strings.SplitN(edge.Source, ".", 2)[0]
		parents[edge.Target] = append(parents[edge.Target], source)
	}
	ancestors := make(map[string]bool)
	queue := append([]string(nil), parents[nodeID]...)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if id == nodeID || ancestors[id] {
			continue
		}
		ancestors[id] = true
		queue = append(queue, parents[id]...)
	}
	// Execution order keeps the original failed prerequisite as the reason,
	// rather than a later write skipped because of that same failure.
	for _, step := range v.ExecutionLogs {
		if step == nil || !ancestors[step.Id] || !isWorkflowWrite(v.TaskNodes[step.Id]) {
			continue
		}
		if !step.Success || step.Error != "" || shouldWaitForContractWriteConfirmation(step, false) || pendingETHTransfer(step) {
			return proto.Clone(step).(*avsproto.Execution_Step)
		}
	}
	return nil
}

func pendingETHTransfer(step *avsproto.Execution_Step) bool {
	if step.GetEthTransfer() == nil || step.Metadata == nil {
		return false
	}
	metadata, ok := step.Metadata.AsInterface().(map[string]interface{})
	return ok && metadata["receiptStatus"] == "pending"
}

// recordSkippedWrite records that no submission was attempted. Success stays
// false, so a successful terminal report cannot turn the workflow green.
// The original step (including receipt/hash and actual effects) stays intact.
func (v *VM) recordSkippedWrite(node *avsproto.TaskNode, prerequisite *avsproto.Execution_Step) {
	reason := prerequisite.Error
	if reason == "" {
		reason = "the prerequisite write did not confirm successfully"
	}
	message := fmt.Sprintf("Skipped: prerequisite write %q (%s) failed: %s", prerequisite.Name, prerequisite.Id, reason)
	step := v.createExecutionStep(node.Id, false, message, "No transaction submitted.", time.Now().UnixMilli())
	step.ErrorCode = avsproto.ErrorCode_EXECUTION_ENGINE_ERROR
	step.Metadata, _ = structpb.NewValue(map[string]interface{}{
		"skipped":            true,
		"prerequisiteNodeId": prerequisite.Id,
		"prerequisiteError":  reason,
	})
	switch {
	case node.GetContractWrite() != nil:
		step.OutputData = &avsproto.Execution_Step_ContractWrite{ContractWrite: &avsproto.ContractWriteNode_Output{}}
	case node.GetEthTransfer() != nil:
		step.OutputData = &avsproto.Execution_Step_EthTransfer{EthTransfer: &avsproto.ETHTransferNode_Output{}}
	case node.GetLoop() != nil:
		step.OutputData = &avsproto.Execution_Step_Loop{Loop: &avsproto.LoopNode_Output{}}
	}
	v.addExecutionLog(step)
}
