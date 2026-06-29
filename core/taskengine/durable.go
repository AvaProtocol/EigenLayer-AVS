package taskengine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
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

// SuspendRequest is recorded by a Suspendable step's runner (Await, HumanApproval)
// to pause the execution. The scheduler stops scheduling new work; the executor
// then checkpoints the run (snapshot vars + status WAITING) and registers Wake in
// the durable registry. AwaitNodeID is the step that suspended.
type SuspendRequest struct {
	AwaitNodeID string
	Wake        *WakeSubscription
}

// requestSuspend records a suspension (called by a step runner during executeNode).
// First-writer-wins: once a step has asked to suspend, later requests are ignored.
func (v *VM) requestSuspend(nodeID string, wake *WakeSubscription) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.suspend == nil {
		v.suspend = &SuspendRequest{AwaitNodeID: nodeID, Wake: wake}
	}
}

// isSuspendRequested reports whether a step asked the execution to suspend.
func (v *VM) isSuspendRequested() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.suspend != nil
}

// PendingSuspend returns the recorded suspension after a Run (nil if the run
// completed normally). The executor consumes this to decide checkpoint vs. finish.
func (v *VM) PendingSuspend() *SuspendRequest {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.suspend
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

	// TaskID is the workflow that owns the suspended execution. Carried here because
	// executions are stored task-scoped (TaskExecutionKey) and there is no global
	// exec→task index, so resuming from a wake (a chain-event notify or boot re-arm)
	// needs the task to load the execution.
	TaskID string `json:"taskId"`

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

// reservedSystemVarNames are VM vars that hold system/secret context, re-injected
// fresh on resume — never node outputs. A node whose (user-chosen) name collides
// with one of these must NOT have its var snapshotted or restored: snapshotting
// would persist secrets (apContext) to disk; restoring would clobber system state.
var reservedSystemVarNames = map[string]bool{
	"apContext":     true, // secrets / ap.* JS context
	"settings":      true, // runner + chain_id
	"aa_sender":     true, // smart-wallet address
	"aa_salt":       true, // wallet salt
	"triggerConfig": true, // trigger config
}

// snapshotNodeVars serializes only the per-node output vars (keyed by node name)
// for a durable suspend. System/secret vars are deliberately excluded — they are
// re-injected fresh on resume, never persisted (apContext carries secrets). This
// is the restorable slice of state that lets a resumed leg reference prior steps'
// outputs ({{node.data.x}}).
func (v *VM) snapshotNodeVars() ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make(map[string]any, len(v.TaskNodes)+1)
	for nodeID := range v.TaskNodes {
		name := v.getNodeNameAsVarLocked(nodeID)
		if name == "" || reservedSystemVarNames[name] {
			continue // never persist a system/secret var, even if a node is named one
		}
		if val, ok := v.vars[name]; ok {
			out[name] = val
		}
	}
	// Include the trigger's output var too (keyed by sanitized trigger name) so a
	// resumed leg can still reference {{trigger.data.x}}.
	if v.task != nil && v.task.Trigger != nil {
		tname := sanitizeTriggerNameForJS(v.task.Trigger.GetName())
		if tname != "" && !reservedSystemVarNames[tname] {
			if val, ok := v.vars[tname]; ok {
				out[tname] = val
			}
		}
	}
	return json.Marshal(out)
}

// restoreNodeVars overlays a snapshot from snapshotNodeVars back onto the VM's
// vars, so resumed nodes resolve prior steps' outputs. UseNumber keeps integers
// from being silently coerced to float64, preserving template-render fidelity.
func (v *VM) restoreNodeVars(snapshot []byte) error {
	if len(snapshot) == 0 {
		return nil
	}
	var restored map[string]any
	dec := json.NewDecoder(bytes.NewReader(snapshot))
	dec.UseNumber()
	if err := dec.Decode(&restored); err != nil {
		return fmt.Errorf("restore node vars: %w", err)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.vars == nil {
		v.vars = make(map[string]any)
	}
	for k, val := range restored {
		if reservedSystemVarNames[k] {
			continue // defense-in-depth: a corrupt/tampered snapshot must not clobber system/secret vars
		}
		v.vars[k] = val
	}
	return nil
}

// completedNodeIDsFromSteps derives the resume completion set from a prior leg's
// persisted execution steps (each executed node appended one step). This is the
// resumeCompleted the re-entrant scheduler consumes.
func completedNodeIDsFromSteps(steps []*avsproto.Execution_Step) map[string]bool {
	out := make(map[string]bool, len(steps))
	for _, s := range steps {
		if s != nil && s.Id != "" {
			out[s.Id] = true
		}
	}
	return out
}

// ---- Durable checkpoint (key: ckpt:<execId>) ---------------------------------
//
// The resumable vars snapshot for a suspended execution (snapshotNodeVars output).
// Paired with the WAITING execution record (which carries the completed steps) and
// the wake subscription. New key template — additive, no migration.

const checkpointPrefix = "ckpt:"

func checkpointKey(execID string) []byte {
	return []byte(checkpointPrefix + execID)
}

func persistCheckpoint(db storage.Storage, execID string, varsSnapshot []byte) error {
	return db.Set(checkpointKey(execID), varsSnapshot)
}

func loadCheckpoint(db storage.Storage, execID string) ([]byte, error) {
	return db.GetKey(checkpointKey(execID))
}

func deleteCheckpoint(db storage.Storage, execID string) error {
	return db.Delete(checkpointKey(execID))
}

// ---- Durable wake registry (key: wake:<execId>) ------------------------------
//
// The persisted set of pending waits. It is the source of truth for restart
// durability: on boot, loadAllWakeSubscriptions rebuilds the in-memory registry,
// which the engine then re-arms to operators (chain), reloads timers, and GCs
// against execution status. New key template — additive, no migration.

const wakeSubscriptionPrefix = "wake:"

func wakeSubscriptionKey(execID string) []byte {
	return []byte(wakeSubscriptionPrefix + execID)
}

// persistedWake is the on-disk shape. ChainEvent is stored as protojson so its
// proto oneofs / well-known types (structpb.Value in contract_abi) round-trip
// faithfully — plain encoding/json cannot serialize those.
type persistedWake struct {
	Kind       WakeKind            `json:"kind"`
	TaskID     string              `json:"taskId"`
	ChainEvent json.RawMessage     `json:"chainEvent,omitempty"`
	External   *ExternalSignalSpec `json:"external,omitempty"`
	TimeoutAt  int64               `json:"timeoutAt"`
}

func marshalWake(sub *WakeSubscription) ([]byte, error) {
	rec := persistedWake{Kind: sub.Kind, TaskID: sub.TaskID, External: sub.External, TimeoutAt: sub.TimeoutAt}
	if sub.ChainEvent != nil {
		b, err := protojson.Marshal(sub.ChainEvent)
		if err != nil {
			return nil, fmt.Errorf("marshal wake chain event: %w", err)
		}
		rec.ChainEvent = b
	}
	return json.Marshal(rec)
}

func unmarshalWake(b []byte) (*WakeSubscription, error) {
	var rec persistedWake
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, err
	}
	sub := &WakeSubscription{Kind: rec.Kind, TaskID: rec.TaskID, External: rec.External, TimeoutAt: rec.TimeoutAt}
	if len(rec.ChainEvent) > 0 {
		cfg := &avsproto.EventTrigger_Config{}
		if err := protojson.Unmarshal(rec.ChainEvent, cfg); err != nil {
			return nil, fmt.Errorf("unmarshal wake chain event: %w", err)
		}
		sub.ChainEvent = cfg
	}
	return sub, nil
}

// persistWakeSubscription writes the wait for execID. Validated first so we never
// persist an inconsistent subscription.
func persistWakeSubscription(db storage.Storage, execID string, sub *WakeSubscription) error {
	if err := sub.Validate(); err != nil {
		return err
	}
	b, err := marshalWake(sub)
	if err != nil {
		return err
	}
	return db.Set(wakeSubscriptionKey(execID), b)
}

// loadAllWakeSubscriptions scans the registry — the boot re-arm entrypoint. The
// engine uses the result to re-register chain waits to operators, reload timers,
// and GC entries whose execution is no longer WAITING.
func loadAllWakeSubscriptions(db storage.Storage, logger ...sdklogging.Logger) (map[string]*WakeSubscription, error) {
	items, err := db.GetByPrefix([]byte(wakeSubscriptionPrefix))
	if err != nil {
		return nil, err
	}
	out := make(map[string]*WakeSubscription, len(items))
	for _, it := range items {
		execID := strings.TrimPrefix(string(it.Key), wakeSubscriptionPrefix)
		sub, uErr := unmarshalWake(it.Value)
		if uErr == nil {
			// Re-validate on load to catch a partially-written / version-mismatched record.
			uErr = sub.Validate()
		}
		if uErr != nil {
			// Skip a corrupt/invalid record — do NOT abort the whole scan. One bad
			// record must never block every timeout sweep and chain-event re-arm on
			// each periodic tick (it would silently freeze durable execution).
			if len(logger) > 0 && logger[0] != nil {
				logger[0].Error("skipping corrupt wake record", "exec_id", execID, "error", uErr)
			}
			continue
		}
		out[execID] = sub
	}
	return out, nil
}

func loadWakeSubscription(db storage.Storage, execID string) (*WakeSubscription, error) {
	b, err := db.GetKey(wakeSubscriptionKey(execID))
	if err != nil {
		return nil, err
	}
	return unmarshalWake(b)
}

func deleteWakeSubscription(db storage.Storage, execID string) error {
	return db.Delete(wakeSubscriptionKey(execID))
}
