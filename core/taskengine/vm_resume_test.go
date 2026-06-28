package taskengine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func customCodeNode(id string) *avsproto.TaskNode {
	return &avsproto.TaskNode{
		Id: id, Name: id,
		TaskType: &avsproto.TaskNode_CustomCode{CustomCode: &avsproto.CustomCodeNode{
			Config: &avsproto.CustomCodeNode_Config{Lang: avsproto.Lang_LANG_JAVASCRIPT, Source: "return { ok: true };"},
		}},
	}
}

// buildLinearCustomCodeVM builds trigger -> cc1 -> cc2 -> cc3 (all self-contained
// CustomCode nodes, no cross-references), compiled and ready to Run.
func buildLinearCustomCodeVM(t *testing.T) (*VM, []string) {
	t.Helper()
	ids := []string{"cc1", "cc2", "cc3"}
	nodes := make([]*avsproto.TaskNode, len(ids))
	for i, id := range ids {
		nodes[i] = &avsproto.TaskNode{
			Id:   id,
			Name: id,
			TaskType: &avsproto.TaskNode_CustomCode{
				CustomCode: &avsproto.CustomCodeNode{
					Config: &avsproto.CustomCodeNode_Config{
						Lang:   avsproto.Lang_LANG_JAVASCRIPT,
						Source: "return { ok: true };",
					},
				},
			},
		}
	}
	trigger := &avsproto.TaskTrigger{Id: "trig", Name: "trig", TriggerType: &avsproto.TaskTrigger_Manual{}}
	edges := []*avsproto.TaskEdge{
		{Id: "e0", Source: trigger.Id, Target: "cc1"},
		{Id: "e1", Source: "cc1", Target: "cc2"},
		{Id: "e2", Source: "cc2", Target: "cc3"},
	}
	task := &model.Workflow{Task: &avsproto.Task{Id: "resume-test", Nodes: nodes, Edges: edges, Trigger: trigger}}

	vm, err := NewVMWithData(task, nil, &config.SmartWalletConfig{}, nil)
	require.NoError(t, err)
	vm.WithLogger(testutil.GetLogger())
	require.NoError(t, vm.Compile())
	return vm, ids
}

func executedNodeIDs(vm *VM) []string {
	out := []string{}
	for _, step := range vm.ExecutionLogs {
		out = append(out, step.Id)
	}
	return out
}

// TestScheduler_FreshRun_RunsAllNodes is the parity baseline: with no resume state,
// every node executes (the dry-replay seed degenerates to the original behavior).
func TestScheduler_FreshRun_RunsAllNodes(t *testing.T) {
	vm, ids := buildLinearCustomCodeVM(t)
	require.NoError(t, vm.Run())
	assert.ElementsMatch(t, ids, executedNodeIDs(vm), "fresh run executes every node")
}

// TestScheduler_Resume_SkipsCompletedNodes proves the re-entrant scheduler:
// marking cc1+cc2 completed makes the resumed run execute ONLY cc3 (the frontier).
// Covers E4 (completed nodes don't re-run) and E5 (resumed run terminates).
func TestScheduler_Resume_SkipsCompletedNodes(t *testing.T) {
	vm, _ := buildLinearCustomCodeVM(t)
	vm.resumeCompleted = map[string]bool{"cc1": true, "cc2": true}

	require.NoError(t, vm.Run(), "resumed run must terminate, not hang (E5)")

	executed := executedNodeIDs(vm)
	assert.Equal(t, []string{"cc3"}, executed, "resume executes only the frontier; cc1/cc2 are not re-run (E4)")
}

// TestScheduler_Resume_EmptyCompletedEqualsFresh asserts the gate: an empty
// resumeCompleted map behaves exactly like a fresh run.
func TestScheduler_Resume_EmptyCompletedEqualsFresh(t *testing.T) {
	vm, ids := buildLinearCustomCodeVM(t)
	vm.resumeCompleted = map[string]bool{} // empty, not nil
	require.NoError(t, vm.Run())
	assert.ElementsMatch(t, ids, executedNodeIDs(vm), "empty completed == fresh run")
}

// TestVM_SnapshotRestoreNodeVars_Fidelity is the fidelity-critical proof for
// increment 4: a node's output survives a snapshot → restore (into a fresh VM)
// such that template resolution is byte-identical — so a resumed leg can read
// {{prior.data.x}}. Also asserts secrets/system vars are NOT snapshotted.
func TestVM_SnapshotRestoreNodeVars_Fidelity(t *testing.T) {
	vm, ids := buildLinearCustomCodeVM(t)
	cc1 := ids[0]

	proc := &CommonProcessor{vm: vm}
	proc.SetOutputVarForStep(cc1, map[string]any{"data": map[string]any{
		"value":  42,
		"msg":    "hi",
		"amount": "1000000000000000000",
	}})

	name := vm.GetNodeNameAsVar(cc1)
	tmpl := "{{" + name + ".data.value}}|{{" + name + ".data.msg}}|{{" + name + ".data.amount}}"
	before := vm.preprocessText(tmpl)
	require.Equal(t, "42|hi|1000000000000000000", before, "sanity: original resolution")

	snap, err := vm.snapshotNodeVars()
	require.NoError(t, err)
	require.Contains(t, string(snap), name, "snapshot includes the node output")
	require.NotContains(t, string(snap), "apContext", "snapshot excludes system/secret vars")

	// Restore into a fresh VM (same task) and assert identical resolution.
	vm2, _ := buildLinearCustomCodeVM(t)
	require.NoError(t, vm2.restoreNodeVars(snap))
	after := vm2.preprocessText(tmpl)
	assert.Equal(t, before, after, "template resolution must be identical after snapshot/restore")
}

func TestCompletedNodeIDsFromSteps(t *testing.T) {
	steps := []*avsproto.Execution_Step{{Id: "a"}, {Id: "b"}, nil, {Id: ""}}
	assert.Equal(t, map[string]bool{"a": true, "b": true}, completedNodeIDsFromSteps(steps))
}

// TestScheduler_Resume_AllCompleted_Terminates guards the empty-frontier case
// (every node already completed). Without the early return it would hang on an
// unclosed channel; the timeout fails fast rather than wedging the suite.
func TestScheduler_Resume_AllCompleted_Terminates(t *testing.T) {
	vm, ids := buildLinearCustomCodeVM(t)
	vm.resumeCompleted = map[string]bool{ids[0]: true, ids[1]: true, ids[2]: true}

	done := make(chan error, 1)
	go func() { done <- vm.Run() }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler hung on an all-completed (empty frontier) resume")
	}
	assert.Empty(t, executedNodeIDs(vm), "no nodes execute when all are completed")
}

// TestScheduler_Suspend_StopsAfterCurrentNode proves the suspend path: a step that
// requests suspension (simulated via requestSuspend on cc1) runs, then the scheduler
// stops — cc2/cc3 are never scheduled — and the suspension is available to the
// executor via PendingSuspend.
func TestScheduler_Suspend_StopsAfterCurrentNode(t *testing.T) {
	vm, _ := buildLinearCustomCodeVM(t)
	vm.requestSuspend("cc1", &WakeSubscription{Kind: WakeTimer, TimeoutAt: 1})

	require.NoError(t, vm.Run())

	assert.Equal(t, []string{"cc1"}, executedNodeIDs(vm), "run stops after the suspending step")
	susp := vm.PendingSuspend()
	require.NotNil(t, susp, "executor can see the pending suspension")
	assert.Equal(t, "cc1", susp.AwaitNodeID)
}

// TestSuspendThenResume_EndToEnd is the in-memory shape of the whole feature
// (minus storage + real signals): leg 1 runs and suspends after cc1; leg 2 restores
// its vars, marks cc1 completed, and resumes — running exactly cc2+cc3.
func TestSuspendThenResume_EndToEnd(t *testing.T) {
	// Leg 1 — run, suspend after cc1, snapshot.
	vm1, _ := buildLinearCustomCodeVM(t)
	vm1.requestSuspend("cc1", &WakeSubscription{Kind: WakeTimer, TimeoutAt: 1})
	require.NoError(t, vm1.Run())
	require.Equal(t, []string{"cc1"}, executedNodeIDs(vm1))
	require.NotNil(t, vm1.PendingSuspend())
	snap, err := vm1.snapshotNodeVars()
	require.NoError(t, err)

	// Leg 2 — resume: restore vars, mark cc1 done, run the rest.
	vm2, _ := buildLinearCustomCodeVM(t)
	require.NoError(t, vm2.restoreNodeVars(snap))
	vm2.resumeCompleted = map[string]bool{"cc1": true}
	require.NoError(t, vm2.Run())
	assert.Equal(t, []string{"cc2", "cc3"}, executedNodeIDs(vm2), "resume runs exactly the remaining steps")
}

// TestExecutor_CheckpointAndResume_DBBacked is the storage-backed proof: the real
// executor method checkpoints a suspended run to BadgerDB (WAITING execution +
// vars checkpoint + wake), then a fresh VM reloads it and resumes to completion.
func TestExecutor_CheckpointAndResume_DBBacked(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()
	// Minimal executor — checkpointSuspendedExecution only needs db + logger (no
	// engine/RPC), so avoid NewExecutorForTesting which dials an RPC.
	executor := &WorkflowExecutor{db: db, logger: testutil.GetLogger()}

	// Leg 1 — run, suspend after cc1.
	vm1, _ := buildLinearCustomCodeVM(t)
	vm1.requestSuspend("cc1", &WakeSubscription{Kind: WakeTimer, TimeoutAt: 999})
	require.NoError(t, vm1.Run())
	require.Equal(t, []string{"cc1"}, executedNodeIDs(vm1))
	susp := vm1.PendingSuspend()
	require.NotNil(t, susp)

	const execID = "exec-durable-1"
	out, err := executor.checkpointSuspendedExecution(vm1.task, &avsproto.Execution{Id: execID, StartAt: 1}, vm1, susp)
	require.NoError(t, err)
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_WAITING, out.Status)

	// Persisted: the WAITING execution record (with its one completed step)...
	raw, err := db.GetKey(TaskExecutionKey(vm1.task, execID))
	require.NoError(t, err)
	loaded := &avsproto.Execution{}
	require.NoError(t, protojson.Unmarshal(raw, loaded))
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_WAITING, loaded.Status)
	assert.Equal(t, "cc1", loaded.ResumeNodeId)
	require.Len(t, loaded.Steps, 1)
	// ...the vars checkpoint...
	ckpt, err := loadCheckpoint(db, execID)
	require.NoError(t, err)
	require.NotEmpty(t, ckpt)
	// ...and the wake subscription.
	wakes, err := loadAllWakeSubscriptions(db)
	require.NoError(t, err)
	require.NotNil(t, wakes[execID])

	// Leg 2 — resume from storage: fresh VM, restore the checkpoint, completed from
	// the persisted steps, run the rest.
	vm2, _ := buildLinearCustomCodeVM(t)
	require.NoError(t, vm2.restoreNodeVars(ckpt))
	vm2.resumeCompleted = completedNodeIDsFromSteps(loaded.Steps)
	require.NoError(t, vm2.Run())
	assert.Equal(t, []string{"cc2", "cc3"}, executedNodeIDs(vm2), "resumed from storage, runs the remaining steps")

	// Terminal cleanup (what advance() does on completion).
	require.NoError(t, deleteCheckpoint(db, execID))
	require.NoError(t, deleteWakeSubscription(db, execID))
	gone, err := loadAllWakeSubscriptions(db)
	require.NoError(t, err)
	assert.Nil(t, gone[execID])
}

// TestExecutor_Advance_ResumesAndFinalizes exercises the production resume
// entrypoint: a stored WAITING execution → Advance() rebuilds the VM, restores,
// runs the rest to a terminal status, and GCs the durable state. Plus idempotency.
func TestExecutor_Advance_ResumesAndFinalizes(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()
	executor := &WorkflowExecutor{db: db, logger: testutil.GetLogger(), smartWalletConfig: &config.SmartWalletConfig{}}

	// Arrange a WAITING execution: run+suspend after cc1, then checkpoint it.
	vm1, _ := buildLinearCustomCodeVM(t)
	vm1.WithDb(db)
	vm1.requestSuspend("cc1", &WakeSubscription{Kind: WakeTimer, TimeoutAt: 999})
	require.NoError(t, vm1.Run())
	const execID = "exec-advance-1"
	_, err := executor.checkpointSuspendedExecution(vm1.task, &avsproto.Execution{Id: execID, StartAt: 1}, vm1, vm1.PendingSuspend())
	require.NoError(t, err)

	// Act: advance (resume).
	out, err := executor.Advance(vm1.task, execID, nil)
	require.NoError(t, err)

	// Resumed to a terminal status, ran the remaining steps, durable state GC'd.
	assert.NotEqual(t, avsproto.ExecutionStatus_EXECUTION_STATUS_WAITING, out.Status)
	var ids []string
	for _, s := range out.Steps {
		ids = append(ids, s.Id)
	}
	assert.Equal(t, []string{"cc1", "cc2", "cc3"}, ids, "resume appended the remaining steps to the record")
	wakes, err := loadAllWakeSubscriptions(db)
	require.NoError(t, err)
	assert.Nil(t, wakes[execID], "wake GC'd on terminal finish")

	// Idempotent: advancing a now-terminal execution is a no-op (E8).
	again, err := executor.Advance(vm1.task, execID, nil)
	require.NoError(t, err)
	assert.Equal(t, out.Status, again.Status)
	assert.Len(t, again.Steps, 3, "no re-execution on a duplicate advance")
}

// TestAwaitNode_SuspendThenSignal_EndToEnd is the whole user-facing feature: a
// workflow [cc1 -> await -> cc2] runs, the Await node suspends it, a delivered
// approval signal (DeliverSignal) resumes it, and cc2 runs. The first real
// Suspendable node, end-to-end through storage.
func TestAwaitNode_SuspendThenSignal_EndToEnd(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()
	executor := &WorkflowExecutor{db: db, logger: testutil.GetLogger(), smartWalletConfig: &config.SmartWalletConfig{}}

	trigger := &avsproto.TaskTrigger{Id: "t", Name: "t", TriggerType: &avsproto.TaskTrigger_Manual{}}
	await := &avsproto.TaskNode{
		Id: "appr", Name: "appr", Type: avsproto.NodeType_NODE_TYPE_AWAIT,
		TaskType: &avsproto.TaskNode_Await{Await: &avsproto.AwaitNode{
			Config: &avsproto.AwaitNode_Config{Channel: "telegram", Approvers: []string{"0xowner"}, Prompt: "approve?", TimeoutSeconds: 3600},
		}},
	}
	task := &model.Workflow{Task: &avsproto.Task{
		Id:      "approval-wf",
		Trigger: trigger,
		Nodes:   []*avsproto.TaskNode{customCodeNode("cc1"), await, customCodeNode("cc2")},
		Edges: []*avsproto.TaskEdge{
			{Id: "e0", Source: "t", Target: "cc1"},
			{Id: "e1", Source: "cc1", Target: "appr"},
			{Id: "e2", Source: "appr", Target: "cc2"},
		},
	}}

	// Leg 1 — run; the Await node suspends after cc1.
	vm1, err := NewVMWithData(task, nil, &config.SmartWalletConfig{}, nil)
	require.NoError(t, err)
	vm1.WithDb(db).WithLogger(testutil.GetLogger())
	require.NoError(t, vm1.Compile())
	require.NoError(t, vm1.Run())
	assert.Equal(t, []string{"cc1", "appr"}, executedNodeIDs(vm1), "ran up to the Await, then suspended")
	susp := vm1.PendingSuspend()
	require.NotNil(t, susp)
	assert.Equal(t, WakeExternalSignal, susp.Wake.Kind)
	assert.Equal(t, "appr", susp.AwaitNodeID)
	assert.Equal(t, "telegram", susp.Wake.External.Channel)

	const execID = "exec-appr-1"
	_, err = executor.checkpointSuspendedExecution(task, &avsproto.Execution{Id: execID, StartAt: 1}, vm1, susp)
	require.NoError(t, err)

	// Leg 2 — deliver the approval signal; the execution resumes and runs cc2.
	payload, err := structpb.NewValue(map[string]any{"decision": "approve", "by": "0xowner"})
	require.NoError(t, err)
	out, err := executor.DeliverSignal(task, &Signal{
		ExecutionID: execID, Kind: WakeExternalSignal, Decision: "approve", Approver: "0xowner", Payload: payload,
	})
	require.NoError(t, err)
	assert.NotEqual(t, avsproto.ExecutionStatus_EXECUTION_STATUS_WAITING, out.Status, "resumed to terminal")
	var ids []string
	for _, s := range out.Steps {
		ids = append(ids, s.Id)
	}
	assert.Equal(t, []string{"cc1", "appr", "cc2"}, ids, "signal resumed the workflow; cc2 ran")

	// Durable state GC'd.
	wakes, err := loadAllWakeSubscriptions(db)
	require.NoError(t, err)
	assert.Nil(t, wakes[execID])
}

// chainEventConfig is a small EventTrigger.Config fixture for chain-event Await tests.
func chainEventConfig(chainID int64) *avsproto.EventTrigger_Config {
	return &avsproto.EventTrigger_Config{
		ChainId: chainID,
		Queries: []*avsproto.EventTrigger_Query{{
			Addresses: []string{"0xbridge0000000000000000000000000000000000"},
			Topics:    []string{"0xMessageReceived00000000000000000000000000000000000000000000000000"},
		}},
	}
}

// TestAwaitNode_ChainEvent_SuspendThenResume proves the cross-chain flavor: an Await
// with a chain_event config suspends with a WakeChainEvent subscription (carrying the
// task id + event filter), and a chain-event Signal (the matched log) resumes it.
func TestAwaitNode_ChainEvent_SuspendThenResume(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()
	executor := &WorkflowExecutor{db: db, logger: testutil.GetLogger(), smartWalletConfig: &config.SmartWalletConfig{}}

	trigger := &avsproto.TaskTrigger{Id: "t", Name: "t", TriggerType: &avsproto.TaskTrigger_Manual{}}
	await := &avsproto.TaskNode{
		Id: "wait", Name: "wait", Type: avsproto.NodeType_NODE_TYPE_AWAIT,
		TaskType: &avsproto.TaskNode_Await{Await: &avsproto.AwaitNode{
			Config: &avsproto.AwaitNode_Config{ChainEvent: chainEventConfig(8453), TimeoutSeconds: 3600},
		}},
	}
	task := &model.Workflow{Task: &avsproto.Task{
		Id:      "bridge-wf",
		Trigger: trigger,
		Nodes:   []*avsproto.TaskNode{customCodeNode("cc1"), await, customCodeNode("cc2")},
		Edges: []*avsproto.TaskEdge{
			{Id: "e0", Source: "t", Target: "cc1"},
			{Id: "e1", Source: "cc1", Target: "wait"},
			{Id: "e2", Source: "wait", Target: "cc2"},
		},
	}}

	// Leg 1 — runs to the Await, then suspends on the chain event.
	vm1, err := NewVMWithData(task, nil, &config.SmartWalletConfig{}, nil)
	require.NoError(t, err)
	vm1.WithDb(db).WithLogger(testutil.GetLogger())
	require.NoError(t, vm1.Compile())
	require.NoError(t, vm1.Run())
	susp := vm1.PendingSuspend()
	require.NotNil(t, susp)
	assert.Equal(t, WakeChainEvent, susp.Wake.Kind)
	require.NotNil(t, susp.Wake.ChainEvent)
	assert.Equal(t, int64(8453), susp.Wake.ChainEvent.GetChainId())

	const execID = "exec-bridge-1"
	_, err = executor.checkpointSuspendedExecution(task, &avsproto.Execution{Id: execID, StartAt: 1}, vm1, susp)
	require.NoError(t, err)

	// The persisted wake carries its task id (so a notify can resolve the task) + the event filter.
	wakes, err := loadAllWakeSubscriptions(db)
	require.NoError(t, err)
	require.NotNil(t, wakes[execID])
	assert.Equal(t, "bridge-wf", wakes[execID].TaskID)
	assert.Equal(t, WakeChainEvent, wakes[execID].Kind)
	require.NotNil(t, wakes[execID].ChainEvent)

	// Leg 2 — the matched chain log arrives as a chain-event signal; the execution resumes.
	eventData, err := structpb.NewValue(map[string]any{"amount": "1000", "messageId": "0xabc"})
	require.NoError(t, err)
	out, err := executor.DeliverSignal(task, &Signal{ExecutionID: execID, Kind: WakeChainEvent, Payload: eventData})
	require.NoError(t, err)
	assert.NotEqual(t, avsproto.ExecutionStatus_EXECUTION_STATUS_WAITING, out.Status, "resumed to terminal")
	var ids []string
	for _, s := range out.Steps {
		ids = append(ids, s.Id)
	}
	assert.Equal(t, []string{"cc1", "wait", "cc2"}, ids, "chain event resumed the workflow; cc2 ran")

	wakes, err = loadAllWakeSubscriptions(db)
	require.NoError(t, err)
	assert.Nil(t, wakes[execID], "wake GC'd after resume")
}

// TestAwaitNode_RejectsAmbiguousConfig proves the flavors are mutually exclusive: an
// Await with both an external-signal channel and a chain_event fails rather than
// silently picking one (and does not suspend).
func TestAwaitNode_RejectsAmbiguousConfig(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()
	trigger := &avsproto.TaskTrigger{Id: "t", Name: "t", TriggerType: &avsproto.TaskTrigger_Manual{}}
	await := &avsproto.TaskNode{
		Id: "wait", Name: "wait", Type: avsproto.NodeType_NODE_TYPE_AWAIT,
		TaskType: &avsproto.TaskNode_Await{Await: &avsproto.AwaitNode{
			Config: &avsproto.AwaitNode_Config{Channel: "telegram", ChainEvent: chainEventConfig(8453)},
		}},
	}
	task := &model.Workflow{Task: &avsproto.Task{
		Id: "ambig-wf", Trigger: trigger,
		Nodes: []*avsproto.TaskNode{customCodeNode("cc1"), await},
		Edges: []*avsproto.TaskEdge{{Id: "e0", Source: "t", Target: "cc1"}, {Id: "e1", Source: "cc1", Target: "wait"}},
	}}
	vm, err := NewVMWithData(task, nil, &config.SmartWalletConfig{}, nil)
	require.NoError(t, err)
	vm.WithDb(db).WithLogger(testutil.GetLogger())
	require.NoError(t, vm.Compile())
	_ = vm.Run()

	assert.Nil(t, vm.PendingSuspend(), "an ambiguous Await must not suspend")
	var awaitStep *avsproto.Execution_Step
	for _, s := range vm.ExecutionLogs {
		if s.Id == "wait" {
			awaitStep = s
		}
	}
	require.NotNil(t, awaitStep, "the Await step is recorded")
	assert.False(t, awaitStep.Success)
	assert.Contains(t, awaitStep.Error, "mutually exclusive")
}

// TestAwaitNode_ChainEvent_ExpireOnTimeout proves the timeout sweep's finalize: a
// WAITING execution whose wake timed out is failed and its durable state GC'd.
func TestAwaitNode_ChainEvent_ExpireOnTimeout(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()
	executor := &WorkflowExecutor{db: db, logger: testutil.GetLogger(), smartWalletConfig: &config.SmartWalletConfig{}}

	trigger := &avsproto.TaskTrigger{Id: "t", Name: "t", TriggerType: &avsproto.TaskTrigger_Manual{}}
	await := &avsproto.TaskNode{
		Id: "wait", Name: "wait", Type: avsproto.NodeType_NODE_TYPE_AWAIT,
		TaskType: &avsproto.TaskNode_Await{Await: &avsproto.AwaitNode{
			Config: &avsproto.AwaitNode_Config{ChainEvent: chainEventConfig(8453), TimeoutSeconds: 3600},
		}},
	}
	task := &model.Workflow{Task: &avsproto.Task{
		Id: "stuck-wf", Trigger: trigger,
		Nodes: []*avsproto.TaskNode{customCodeNode("cc1"), await, customCodeNode("cc2")},
		Edges: []*avsproto.TaskEdge{
			{Id: "e0", Source: "t", Target: "cc1"},
			{Id: "e1", Source: "cc1", Target: "wait"},
			{Id: "e2", Source: "wait", Target: "cc2"},
		},
	}}

	vm1, err := NewVMWithData(task, nil, &config.SmartWalletConfig{}, nil)
	require.NoError(t, err)
	vm1.WithDb(db).WithLogger(testutil.GetLogger())
	require.NoError(t, vm1.Compile())
	require.NoError(t, vm1.Run())
	susp := vm1.PendingSuspend()
	require.NotNil(t, susp)

	const execID = "exec-stuck-1"
	_, err = executor.checkpointSuspendedExecution(task, &avsproto.Execution{Id: execID, StartAt: 1}, vm1, susp)
	require.NoError(t, err)

	// The wait times out: expire it. cc2 never ran; the execution is FAILED, state GC'd.
	out, err := executor.ExpireWait(task, execID)
	require.NoError(t, err)
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED, out.Status)
	assert.Contains(t, out.Error, "timed out")
	var ids []string
	for _, s := range out.Steps {
		ids = append(ids, s.Id)
	}
	assert.Equal(t, []string{"cc1", "wait"}, ids, "cc2 did not run — the wait expired")
	wakes, err := loadAllWakeSubscriptions(db)
	require.NoError(t, err)
	assert.Nil(t, wakes[execID], "wake GC'd after expiry")

	// Idempotent: expiring again just returns the terminal record.
	again, err := executor.ExpireWait(task, execID)
	require.NoError(t, err)
	assert.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED, again.Status)
}

// TestSweepExpiredWaits_TimeGating proves the sweep only touches wakes past their
// timeout, and GCs a wake whose task is gone (orphan).
func TestSweepExpiredWaits_TimeGating(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()
	n := New(db, testutil.GetAggregatorConfig(), nil, testutil.GetLogger())

	// A future wake is left alone; an expired orphan (task gone) is GC'd.
	const farFuture = int64(99999999999999) // unix ms, ~year 5138
	require.NoError(t, persistWakeSubscription(db, "exec-future", &WakeSubscription{
		Kind: WakeChainEvent, TaskID: "wf-x", ChainEvent: chainEventConfig(8453), TimeoutAt: farFuture,
	}))
	require.NoError(t, persistWakeSubscription(db, "exec-expired-orphan", &WakeSubscription{
		Kind: WakeChainEvent, TaskID: "wf-gone", ChainEvent: chainEventConfig(8453), TimeoutAt: 1,
	}))

	n.sweepExpiredWaits()

	wakes, err := loadAllWakeSubscriptions(db)
	require.NoError(t, err)
	assert.NotNil(t, wakes["exec-future"], "future wake untouched")
	assert.Nil(t, wakes["exec-expired-orphan"], "expired orphan wake GC'd")
}

// TestValidateAwaitOperatorCoverage proves the create-time guard: a chain-event Await
// whose chain no operator covers is rejected (gateway mode); an external-signal Await
// has no chain and is fine.
func TestValidateAwaitOperatorCoverage(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()
	cfg := testutil.GetAggregatorConfig()
	cfg.IsGateway = true // the guard only runs in gateway mode
	n := New(db, cfg, nil, testutil.GetLogger())

	awaitNode := func(c *avsproto.AwaitNode_Config) *model.Workflow {
		return &model.Workflow{Task: &avsproto.Task{Nodes: []*avsproto.TaskNode{{
			Id: "wait", Type: avsproto.NodeType_NODE_TYPE_AWAIT,
			TaskType: &avsproto.TaskNode_Await{Await: &avsproto.AwaitNode{Config: c}},
		}}}}
	}

	// Chain-event Await, no operator connected for the chain → rejected.
	err := n.validateAwaitOperatorCoverage(awaitNode(&avsproto.AwaitNode_Config{ChainEvent: chainEventConfig(8453)}))
	require.Error(t, err)
	assert.Equal(t, codes.FailedPrecondition, status.Code(err))

	// External-signal Await has no chain → allowed regardless of operators.
	require.NoError(t, n.validateAwaitOperatorCoverage(awaitNode(&avsproto.AwaitNode_Config{Channel: "telegram"})))
}

// TestInternalTrigger_SyncAndRoute proves the operator-transport seam: a chain-event
// wake surfaces as a synthetic event-trigger "task" (pendingChainEventTriggers) that
// the operator sync streams, and an internal-trigger id round-trips so the notify
// path can route it back. A notify with no live wake is a safe no-op.
func TestInternalTrigger_SyncAndRoute(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()
	n := New(db, testutil.GetAggregatorConfig(), nil, testutil.GetLogger())

	// id round-trip; a real ULID-style id is not mistaken for an internal trigger.
	const execID = "exec-itrig-1"
	id := internalTriggerTaskID(execID)
	got, ok := parseInternalTriggerTaskID(id)
	require.True(t, ok)
	assert.Equal(t, execID, got)
	_, ok = parseInternalTriggerTaskID("01HZY8M0000000000000000000")
	assert.False(t, ok, "a normal task id is not an internal trigger")

	// A persisted chain-event wake surfaces as a synthetic single-shot event trigger.
	require.NoError(t, persistWakeSubscription(db, execID, &WakeSubscription{
		Kind: WakeChainEvent, TaskID: "wf-1", ChainEvent: chainEventConfig(8453), TimeoutAt: 1 << 40,
	}))
	// An external-signal wake must NOT surface (it's not operator-watched).
	require.NoError(t, persistWakeSubscription(db, "exec-ext-1", &WakeSubscription{
		Kind: WakeExternalSignal, TaskID: "wf-2", External: &ExternalSignalSpec{Channel: "telegram"}, TimeoutAt: 1 << 40,
	}))

	triggers := n.pendingChainEventTriggers()
	require.Len(t, triggers, 1, "only the chain-event wake yields an internal trigger")
	syn := triggers[0]
	assert.Equal(t, id, syn.Id)
	assert.Equal(t, int64(8453), syn.Trigger.GetEvent().GetConfig().GetChainId())
	assert.Equal(t, int64(1<<40), syn.ExpiredAt)

	// A notify for an execution with no live wake is a safe no-op, not an error.
	state, err := n.deliverChainEventWake("exec-gone", &avsproto.NotifyTriggersReq{})
	require.NoError(t, err)
	assert.Equal(t, "not_found", state.Status)
}

// TestSignalExecution_AuthGates proves the engine-level transport gates map to the
// right gRPC codes (so the REST layer returns 400 vs 404, not 500): an invalid
// decision is InvalidArgument; a signal from a non-owner is NotFound (the ownership
// gate — User2 cannot signal User1's workflow). The full suspend→signal→resume path
// is covered by TestAwaitNode_SuspendThenSignal_EndToEnd + the Advance tests.
func TestSignalExecution_AuthGates(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()
	n := New(db, testutil.GetAggregatorConfig(), nil, testutil.GetLogger())

	// Invalid decision is rejected up front, before any workflow lookup.
	_, err := n.SignalExecution(testutil.TestUser1(), "any-workflow", "exec-1", "maybe", nil)
	require.Error(t, err)
	assert.Equal(t, codes.InvalidArgument, status.Code(err), "invalid decision → InvalidArgument")

	// User1 owns a workflow; User2 must not be able to signal it.
	created, err := n.CreateWorkflow(testutil.TestUser1(), testutil.RestTask())
	require.NoError(t, err)
	_, err = n.SignalExecution(testutil.TestUser2(), created.Id, "exec-1", "approve", nil)
	require.Error(t, err)
	assert.Equal(t, codes.NotFound, status.Code(err), "non-owner signal → NotFound (ownership gate)")
}

// TestSnapshotNodeVars_ExcludesReservedNames guards the reserved-name collision:
// a node literally named a system var (apContext) must not be snapshotted, or a
// resume could persist secrets to disk.
func TestSnapshotNodeVars_ExcludesReservedNames(t *testing.T) {
	node := &avsproto.TaskNode{
		Id: "apContext", Name: "apContext",
		TaskType: &avsproto.TaskNode_CustomCode{CustomCode: &avsproto.CustomCodeNode{
			Config: &avsproto.CustomCodeNode_Config{Lang: avsproto.Lang_LANG_JAVASCRIPT, Source: "return {};"},
		}},
	}
	trigger := &avsproto.TaskTrigger{Id: "t", Name: "t", TriggerType: &avsproto.TaskTrigger_Manual{}}
	task := &model.Workflow{Task: &avsproto.Task{
		Id: "x", Nodes: []*avsproto.TaskNode{node}, Trigger: trigger,
		Edges: []*avsproto.TaskEdge{{Id: "e", Source: "t", Target: "apContext"}},
	}}
	vm, err := NewVMWithData(task, nil, &config.SmartWalletConfig{}, nil)
	require.NoError(t, err)

	proc := &CommonProcessor{vm: vm}
	proc.SetOutputVarForStep("apContext", map[string]any{"data": map[string]any{"secret": "leak"}})

	snap, err := vm.snapshotNodeVars()
	require.NoError(t, err)
	assert.NotContains(t, string(snap), "leak", "the apContext node's secret value must not be snapshotted")
	assert.NotContains(t, string(snap), "apContext", "a node named apContext must be excluded from the snapshot")
}

// TestResume_RestoredVarsUsableByFrontier ties it together: restore a prior leg's
// node vars into a fresh VM, mark them completed, and run — the frontier node
// executes (only it) with the restored vars available. This is the in-memory
// shape of advance(); the storage-backed wiring is the remaining step.
func TestResume_RestoredVarsUsableByFrontier(t *testing.T) {
	// Leg 1: run fully, snapshot the node vars.
	vm1, ids := buildLinearCustomCodeVM(t)
	require.NoError(t, vm1.Run())
	require.Len(t, executedNodeIDs(vm1), 3)
	snap, err := vm1.snapshotNodeVars()
	require.NoError(t, err)

	// Leg 2 (resume): fresh VM, restore vars, mark cc1+cc2 done, run.
	vm2, _ := buildLinearCustomCodeVM(t)
	require.NoError(t, vm2.restoreNodeVars(snap))
	vm2.resumeCompleted = completedNodeIDsFromSteps([]*avsproto.Execution_Step{{Id: ids[0]}, {Id: ids[1]}})
	require.NoError(t, vm2.Run())

	assert.Equal(t, []string{ids[2]}, executedNodeIDs(vm2), "only the frontier runs on resume")
	// The restored prior output is present and resolvable.
	assert.Contains(t, vm2.preprocessText("{{"+vm2.GetNodeNameAsVar(ids[0])+".data.ok}}"), "true",
		"restored prior-node output is readable by the resumed leg")
}
