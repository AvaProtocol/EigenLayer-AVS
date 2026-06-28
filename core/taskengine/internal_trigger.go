package taskengine

import (
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/protobuf/types/known/structpb"
)

// internalTriggerPrefix namespaces the synthetic task IDs that stand in for a
// suspended execution's chain-event wake. Real task IDs are ULIDs (base32, no
// underscore), so this prefix can never collide with a user workflow ID — and it
// lets the notify path tell an internal-trigger notification apart from a normal
// task one by the ID alone.
const internalTriggerPrefix = "itrig_"

// internalTriggerTaskID is the synthetic task ID an operator watches for a
// chain-event wake. The execution ID is embedded so the operator's NotifyTriggers
// round-trips straight back to the waiting execution.
func internalTriggerTaskID(executionID string) string {
	return internalTriggerPrefix + executionID
}

// parseInternalTriggerTaskID reports whether taskID is an internal-trigger ID and,
// if so, returns the execution ID it carries.
func parseInternalTriggerTaskID(taskID string) (executionID string, ok bool) {
	if strings.HasPrefix(taskID, internalTriggerPrefix) {
		return strings.TrimPrefix(taskID, internalTriggerPrefix), true
	}
	return "", false
}

// pendingChainEventTriggers builds one synthetic, single-shot EventTrigger "task"
// per WAITING execution that is waiting on a chain event. They are streamed to
// operators through the very same sync path as user tasks, so the operator watches
// them with zero new transport. Re-derived from storage on every sync, so a boot or
// operator reconnect re-arms every live wait for free.
func (n *Engine) pendingChainEventTriggers() []*model.Workflow {
	wakes, err := loadAllWakeSubscriptions(n.db)
	if err != nil {
		n.logger.Error("internal-trigger sync: load wake subscriptions", "error", err)
		return nil
	}
	out := make([]*model.Workflow, 0, len(wakes))
	for executionID, wake := range wakes {
		if wake.Kind != WakeChainEvent || wake.ChainEvent == nil {
			continue
		}
		id := internalTriggerTaskID(executionID)
		out = append(out, &model.Workflow{Task: &avsproto.Task{
			Id:           id,
			Status:       avsproto.TaskStatus_Enabled,
			MaxExecution: 1,
			ExpiredAt:    wake.TimeoutAt, // operator stops watching past the wait's timeout
			Trigger: &avsproto.TaskTrigger{
				Id:   id,
				Name: "await",
				TriggerType: &avsproto.TaskTrigger_Event{
					Event: &avsproto.EventTrigger{Config: wake.ChainEvent},
				},
			},
		}})
	}
	return out
}

// deliverChainEventWake routes an operator's notification for an internal trigger
// (itrig_<execId>) to its suspended execution: resolve the task from the wake, build
// a chain-event Signal from the matched event output, and advance the execution.
// A duplicate or late fire is a safe no-op — the wake is gone once resumed, and
// Advance only acts on a still-WAITING execution.
func (n *Engine) deliverChainEventWake(executionID string, payload *avsproto.NotifyTriggersReq) (*ExecutionState, error) {
	wake, err := loadWakeSubscription(n.db, executionID)
	if err != nil {
		// No pending wait — already resumed, timed out, or GC'd. Nothing to do.
		return &ExecutionState{Status: "not_found", Message: "no pending wait for " + executionID}, nil
	}
	task, err := n.GetWorkflowByID(wake.TaskID)
	if err != nil {
		return &ExecutionState{Status: "not_found", Message: "task " + wake.TaskID + " for wake " + executionID + " not found"}, nil
	}

	var eventOutput *structpb.Value
	if out, ok := ExtractTriggerOutput(payload.TriggerOutput).(*avsproto.EventTrigger_Output); ok && out != nil {
		eventOutput = out.GetData()
	}

	executor := NewExecutor(n.ResolveSmartWalletConfig(n.defaultChainID()), n.db, n.logger, n, n.priceService)
	signal := &Signal{ExecutionID: executionID, Kind: WakeChainEvent, Payload: eventOutput}
	if _, err := executor.DeliverSignal(task, signal); err != nil {
		n.logger.Error("internal-trigger wake: deliver signal", "execution_id", executionID, "error", err)
		return &ExecutionState{Status: "error", Message: err.Error()}, err
	}
	return &ExecutionState{Status: "wake_delivered", TaskStillEnabled: true, Message: "resumed execution " + executionID}, nil
}
