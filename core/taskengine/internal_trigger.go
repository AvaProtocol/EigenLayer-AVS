package taskengine

import (
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
)

// validateAwaitOperatorCoverage rejects, at create time, a chain-event Await whose
// chain no operator currently covers — the wait could never be observed, so it would
// never resume. Gateway-only (where operators advertise chain coverage); a transient
// FailedPrecondition, not a malformed request, since coverage is connection state.
func (n *Engine) validateAwaitOperatorCoverage(task *model.Workflow) error {
	if n.config == nil || !n.config.IsGateway || task == nil {
		return nil
	}
	for _, node := range task.Nodes {
		await := node.GetAwait()
		if await == nil || await.Config == nil {
			continue
		}
		ce := await.Config.GetChainEvent()
		if ce == nil {
			continue // external-signal flavor needs no operator
		}
		// operatorsCoveringChain reads trackSyncedTasks and requires n.lock.
		n.lock.Lock()
		covered := len(n.operatorsCoveringChain(ce.GetChainId()))
		n.lock.Unlock()
		if covered == 0 {
			return status.Errorf(codes.FailedPrecondition,
				"await node waits on chain_id=%d but no operator currently covers it; the wait could never fire", ce.GetChainId())
		}
	}
	return nil
}

// deregisterInternalTrigger tells operators to stop watching a consumed internal
// trigger (the wake it stood for is gone). If the execution re-suspended on a new
// chain-event wake, the next sync re-registers it (DeleteTask also untracks it).
func (n *Engine) deregisterInternalTrigger(executionID string) {
	n.notifyOperatorsTaskOperation(internalTriggerTaskID(executionID), avsproto.MessageOp_DeleteTask)
}

// sweepExpiredWaits fails WAITING executions whose wake timed out (a stuck bridge, an
// approver who never responded) and GCs their durable state. Runs on the periodic
// scan loop, so a wait whose timeout passed while no operator was up still resolves.
func (n *Engine) sweepExpiredWaits() {
	wakes, err := loadAllWakeSubscriptions(n.db)
	if err != nil {
		n.logger.Error("sweep expired waits: load wakes", "error", err)
		return
	}
	now := time.Now().UnixMilli()
	var executor *WorkflowExecutor
	for executionID, wake := range wakes {
		if wake.TimeoutAt <= 0 || wake.TimeoutAt > now {
			continue
		}
		task, err := n.GetWorkflowByID(wake.TaskID)
		if err != nil {
			// Only GC when the task is genuinely gone. On a corrupted/transient error,
			// preserve the durable state so the problem stays diagnosable + recoverable.
			if status.Code(err) == codes.NotFound {
				_ = deleteCheckpoint(n.db, executionID)
				_ = deleteWakeSubscription(n.db, executionID)
			} else {
				n.logger.Error("sweep expired waits: load task", "task_id", wake.TaskID, "execution_id", executionID, "error", err)
			}
			continue
		}
		if executor == nil {
			executor = NewExecutor(n.ResolveSmartWalletConfig(n.defaultChainID()), n.db, n.logger, n, n.priceService)
		}
		if _, err := executor.ExpireWait(task, executionID); err != nil {
			n.logger.Error("sweep expired waits: expire", "execution_id", executionID, "error", err)
			continue
		}
		// Deregistration only applies to chain-event waits (operator-watched triggers).
		if wake.Kind == WakeChainEvent {
			n.deregisterInternalTrigger(executionID)
		}
		n.logger.Info("expired timed-out wait", "execution_id", executionID, "task_id", wake.TaskID)
	}
}

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
	// The wake it watched is consumed — tell operators to stop. A re-suspend on a new
	// chain-event wake re-registers via the next sync.
	n.deregisterInternalTrigger(executionID)
	return &ExecutionState{Status: "wake_delivered", TaskStillEnabled: true, Message: "resumed execution " + executionID}, nil
}
