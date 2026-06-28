package taskengine

import (
	"fmt"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// defaultAwaitTimeoutSeconds bounds an Await with no explicit timeout. Every wait
// is bounded — an unbounded human-approval gate would park funds forever.
const defaultAwaitTimeoutSeconds = 86400 // 24h

// runAwait executes an Await node: it builds the wake subscription from config and
// requests suspension (the scheduler then stops; the executor checkpoints +
// registers the wake). The runner runs exactly once, on the suspend pass — on
// resume the node is in resumeCompleted and Advance injects the delivered signal
// as this node's output.
func (v *VM) runAwait(node *avsproto.TaskNode) (*avsproto.Execution_Step, error) {
	t0 := time.Now()
	step := &avsproto.Execution_Step{
		Id:      node.Id,
		Type:    avsproto.NodeType_NODE_TYPE_AWAIT.String(),
		Name:    node.Name,
		StartAt: t0.UnixMilli(),
	}

	cfg := node.GetAwait().GetConfig()
	if cfg == nil {
		err := fmt.Errorf("await node %s has no config", node.Id)
		step.Success = false
		step.Error = err.Error()
		step.EndAt = time.Now().UnixMilli()
		return step, err
	}

	// The two flavors are mutually exclusive — reject an ambiguous config rather than
	// silently preferring one (the proto can't express the XOR).
	hasExternal := cfg.GetChannel() != "" || len(cfg.GetApprovers()) > 0 || cfg.GetPrompt() != ""
	if cfg.GetChainEvent() != nil && hasExternal {
		err := fmt.Errorf("await node %s sets both chain_event and external-signal fields; they are mutually exclusive", node.Id)
		step.Success = false
		step.Error = err.Error()
		step.EndAt = time.Now().UnixMilli()
		return step, err
	}

	timeoutSec := int64(cfg.GetTimeoutSeconds())
	if timeoutSec <= 0 {
		timeoutSec = defaultAwaitTimeoutSeconds
	}
	timeoutAt := t0.Add(time.Duration(timeoutSec) * time.Second).UnixMilli()

	// Two flavors: a chain-event wake (cross-chain — an operator watches the event)
	// or an external-signal wake (human approval). chain_event selects the former.
	var wake *WakeSubscription
	if ev := cfg.GetChainEvent(); ev != nil {
		wake = &WakeSubscription{
			Kind:       WakeChainEvent,
			ChainEvent: ev,
			TimeoutAt:  timeoutAt,
		}
	} else {
		wake = &WakeSubscription{
			Kind: WakeExternalSignal,
			External: &ExternalSignalSpec{
				Channel:   cfg.GetChannel(),
				Approvers: cfg.GetApprovers(),
				Prompt:    cfg.GetPrompt(),
			},
			TimeoutAt: timeoutAt,
		}
	}
	if err := wake.Validate(); err != nil {
		// A misconfigured Await fails the step rather than suspending.
		step.Success = false
		step.Error = err.Error()
		step.EndAt = time.Now().UnixMilli()
		return step, err
	}

	v.requestSuspend(node.Id, wake)

	step.Success = true
	if wake.Kind == WakeChainEvent {
		step.Log = fmt.Sprintf("awaiting chain event on chain %d", wake.ChainEvent.GetChainId())
	} else {
		step.Log = fmt.Sprintf("awaiting external signal on channel %q", cfg.GetChannel())
	}
	step.EndAt = time.Now().UnixMilli()
	return step, nil
}
