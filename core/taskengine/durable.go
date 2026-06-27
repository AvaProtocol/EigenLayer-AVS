package taskengine

import (
	"fmt"

	"google.golang.org/protobuf/types/known/structpb"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// Durable execution vocabulary (Level-3 engine — see PLAN_DURABLE_EXECUTION.md).
//
// These are pure type definitions: the foundation the re-entrant executor
// (advance) and the suspendable steps (Await, HumanApproval) build on. Nothing
// wires them yet — increment 2 establishes the vocabulary; later increments use it.

// RunDisposition is what the executor should do after a step's run returns. The
// core of the durable model is that any step may ask to suspend, not just fail or
// continue.
type RunDisposition int

const (
	RunContinue RunDisposition = iota // normal completion; schedule successors
	RunSuspend                        // pause: persist WAITING + register the wake subscription
	RunFail                           // fatal: terminate the execution as FAILED
)

func (d RunDisposition) String() string {
	switch d {
	case RunContinue:
		return "continue"
	case RunSuspend:
		return "suspend"
	case RunFail:
		return "fail"
	default:
		return fmt.Sprintf("RunDisposition(%d)", int(d))
	}
}

// WakeKind identifies which signal source can advance a suspended execution.
type WakeKind int

const (
	WakeChainEvent     WakeKind = iota // operator-watched event on a chain (cross-chain Await)
	WakeExternalSignal                 // gateway-received signal (Telegram approval, webhook)
	WakeTimer                          // scheduler due-time (delay, approval timeout)
)

func (k WakeKind) String() string {
	switch k {
	case WakeChainEvent:
		return "chain_event"
	case WakeExternalSignal:
		return "external_signal"
	case WakeTimer:
		return "timer"
	default:
		return fmt.Sprintf("WakeKind(%d)", int(k))
	}
}

// WakeSubscription is what a suspended execution is waiting for. Exactly one
// source field is populated, matching Kind. Persisted (key wake:<execId>) while
// the execution is WAITING, and matched by an inbound Signal to advance it.
type WakeSubscription struct {
	Kind WakeKind `json:"kind"`

	// ChainEvent is the event filter the operator watches (chain = ChainEvent's
	// chain_id). Set iff Kind == WakeChainEvent. Reuses the existing event-trigger
	// config so the operator watches it with no new logic.
	ChainEvent *avsproto.EventTrigger_Config `json:"chainEvent,omitempty"`

	// External describes who/what may deliver the wake. Set iff Kind == WakeExternalSignal.
	External *ExternalSignalSpec `json:"external,omitempty"`

	// TimeoutAt bounds every wait (unix ms). For Kind == WakeTimer it is the fire
	// time; for the other kinds it is the hard timeout after which the wait expires.
	TimeoutAt int64 `json:"timeoutAt"`
}

// ExternalSignalSpec describes an externally-delivered wake (human approval, webhook).
// Authorization is anchored on the deploy-time EOA signature (the bounded envelope);
// the signal just provides the go/no-go within it. See the approval security model
// in PLAN_DURABLE_EXECUTION.md.
type ExternalSignalSpec struct {
	Channel   string   `json:"channel"`             // "telegram" | "api"
	Approvers []string `json:"approvers,omitempty"` // authorized parties; empty ⇒ the workflow owner
	Prompt    string   `json:"prompt,omitempty"`
}

// Validate asserts the subscription is internally consistent: exactly the source
// field matching Kind is populated, and a timeout is set.
func (w *WakeSubscription) Validate() error {
	if w == nil {
		return fmt.Errorf("nil wake subscription")
	}
	switch w.Kind {
	case WakeChainEvent:
		if w.ChainEvent == nil {
			return fmt.Errorf("chain-event wake requires ChainEvent config")
		}
		if w.External != nil {
			return fmt.Errorf("chain-event wake must not set External")
		}
	case WakeExternalSignal:
		if w.External == nil || w.External.Channel == "" {
			return fmt.Errorf("external-signal wake requires External with a channel")
		}
		if w.ChainEvent != nil {
			return fmt.Errorf("external-signal wake must not set ChainEvent")
		}
	case WakeTimer:
		if w.ChainEvent != nil || w.External != nil {
			return fmt.Errorf("timer wake must not set ChainEvent/External")
		}
	default:
		return fmt.Errorf("unknown wake kind %d", int(w.Kind))
	}
	if w.TimeoutAt <= 0 {
		return fmt.Errorf("wake subscription requires a positive TimeoutAt (every wait is bounded)")
	}
	return nil
}

// Signal is an inbound wake delivered to one waiting execution via
// advance(executionId, signal). Its Payload becomes the suspended step's output.
type Signal struct {
	ExecutionID string    `json:"executionId"`
	Kind        WakeKind  `json:"kind"`
	// Decision is set for external/approval signals: "approve" | "reject".
	Decision string `json:"decision,omitempty"`
	// Approver identifies who delivered an external signal (audit + authorization).
	Approver string `json:"approver,omitempty"`
	// Payload carries the wake event (e.g. the matched chain log) as structured data
	// that becomes the suspended step's output, readable by downstream steps.
	Payload *structpb.Value `json:"-"`
}

// Validate asserts the signal can be routed to a waiting execution.
func (s *Signal) Validate() error {
	if s == nil {
		return fmt.Errorf("nil signal")
	}
	if s.ExecutionID == "" {
		return fmt.Errorf("signal requires an executionId")
	}
	if s.Kind == WakeExternalSignal && s.Decision == "" {
		return fmt.Errorf("external signal requires a decision")
	}
	return nil
}
