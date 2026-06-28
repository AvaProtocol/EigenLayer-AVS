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

// validateAwaitConfigs rejects, at create time, an Await node with an invalid config:
// the external-signal flavor (a channel) and the chain-event flavor are mutually
// exclusive, and exactly one must be set (an external-signal Await requires a channel).
// Without this an invalid Await would persist and only fail later at execution in
// runAwait / WakeSubscription.Validate. Runs in every mode (it's a config error, not a
// coverage condition).
func (n *Engine) validateAwaitConfigs(task *model.Workflow) error {
	if task == nil {
		return nil
	}
	for _, node := range task.Nodes {
		await := node.GetAwait()
		if await == nil {
			continue
		}
		// An Await whose name is a reserved system var would, on resume, overwrite that
		// var (the delivered signal is set under the node's name) — reject it at create.
		if reservedSystemVarNames[node.GetName()] {
			return status.Errorf(codes.InvalidArgument,
				"await node name %q collides with a reserved system variable; rename it", node.GetName())
		}
		cfg := await.GetConfig()
		if cfg == nil {
			return status.Errorf(codes.InvalidArgument, "await node %s has no config", node.GetId())
		}
		hasChainEvent := cfg.GetChainEvent() != nil
		hasExternalFields := cfg.GetChannel() != "" || len(cfg.GetApprovers()) > 0 || cfg.GetPrompt() != ""
		if hasChainEvent && hasExternalFields {
			return status.Errorf(codes.InvalidArgument,
				"await node %s sets both an external-signal field and chain_event; they are mutually exclusive", node.GetId())
		}
		if hasChainEvent {
			continue // chain-event flavor is valid
		}
		// External-signal flavor — channel is required (approvers/prompt alone is invalid).
		if cfg.GetChannel() == "" {
			return status.Errorf(codes.InvalidArgument,
				"await node %s requires either an external-signal 'channel' or 'chain_event'", node.GetId())
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

// gcDurableStateForTask removes the durable state (checkpoint + wake, plus the operator
// internal trigger for chain-event waits) of every suspended execution belonging to
// taskID. Called when a workflow is deleted so a WAITING execution's wake doesn't
// linger until the timeout sweep — and so external-signal waits (which the sync-time GC
// never sees) are cleaned up promptly too.
func (n *Engine) gcDurableStateForTask(taskID string) {
	wakes, err := loadAllWakeSubscriptions(n.db, n.logger)
	if err != nil {
		n.logger.Error("gc durable state for deleted task: load wakes", "task_id", taskID, "error", err)
		return
	}
	for executionID, wake := range wakes {
		if wake.TaskID != taskID {
			continue
		}
		_ = deleteCheckpoint(n.db, executionID)
		_ = deleteWakeSubscription(n.db, executionID)
		if wake.Kind == WakeChainEvent {
			n.deregisterInternalTrigger(executionID)
		}
	}
}

// sweepExpiredWaits fails WAITING executions whose wake timed out (a stuck bridge, an
// approver who never responded) and GCs their durable state. Runs on the periodic
// scan loop, so a wait whose timeout passed while no operator was up still resolves.
func (n *Engine) sweepExpiredWaits() {
	wakes, err := loadAllWakeSubscriptions(n.db, n.logger)
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
		// Serialize against a concurrent resume of the same execution (exactly-once):
		// a signal landing as the sweep expires must not double-finalize.
		mu := n.executionMutex(executionID)
		mu.Lock()
		_, expireErr := executor.ExpireWait(task, executionID)
		mu.Unlock()
		if expireErr != nil {
			n.logger.Error("sweep expired waits: expire", "execution_id", executionID, "error", expireErr)
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
	wakes, err := loadAllWakeSubscriptions(n.db, n.logger)
	if err != nil {
		n.logger.Error("internal-trigger sync: load wake subscriptions", "error", err)
		return nil
	}
	out := make([]*model.Workflow, 0, len(wakes))
	for executionID, wake := range wakes {
		if wake.Kind != WakeChainEvent || wake.ChainEvent == nil {
			continue
		}
		// GC a wake whose workflow has been deleted (e.g. DELETE /workflows/{id})
		// instead of streaming an internal trigger that can never resume — otherwise
		// operators keep watching it until the timeout sweep eventually clears it.
		// NotFound only; a transient/corrupt error preserves the state for recovery.
		if _, err := n.GetWorkflowByID(wake.TaskID); err != nil {
			if status.Code(err) == codes.NotFound {
				_ = deleteCheckpoint(n.db, executionID)
				_ = deleteWakeSubscription(n.db, executionID)
			}
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
	// Serialize against any other resume of this execution (a duplicate operator notify
	// or a concurrent external signal) so the load-wake → run sequence is exactly-once.
	// The gate read below must be inside the lock: the loser then finds no wake and no-ops.
	mu := n.executionMutex(executionID)
	mu.Lock()
	defer mu.Unlock()

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
