package taskengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

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
