package taskengine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
