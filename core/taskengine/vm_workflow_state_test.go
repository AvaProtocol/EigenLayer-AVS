package taskengine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// runCustomCodeForState executes `source` as a customCode node inside a task with
// the given taskID, wired to db. Returns after asserting the step succeeded.
func runCustomCodeForState(t *testing.T, db storage.Storage, taskID, source string, simulation bool) {
	t.Helper()
	taskNode := &avsproto.TaskNode{
		Id:   "code1",
		Name: "code1",
		TaskType: &avsproto.TaskNode_CustomCode{
			CustomCode: &avsproto.CustomCodeNode{
				Config: &avsproto.CustomCodeNode_Config{
					Lang:   avsproto.Lang_LANG_JAVASCRIPT,
					Source: source,
				},
			},
		},
	}
	trigger := &avsproto.TaskTrigger{Id: "trigger", Name: "trigger"}
	vm, err := NewVMWithData(&model.Workflow{
		Task: &avsproto.Task{
			Id:      taskID,
			Nodes:   []*avsproto.TaskNode{taskNode},
			Edges:   []*avsproto.TaskEdge{{Id: "e1", Source: trigger.Id, Target: "code1"}},
			Trigger: trigger,
		},
	}, nil, testutil.GetTestSmartWalletConfig(), nil)
	require.NoError(t, err)
	vm.WithDb(db)
	if simulation {
		vm.SetSimulation(true)
	}

	proc := NewJSProcessor(vm)
	step, err := proc.Execute("code1", taskNode.GetCustomCode())
	require.NoError(t, err)
	require.True(t, step.Success, "customCode step failed: %s", step.Error)
}

func readStateMap(t *testing.T, db storage.Storage, taskID, stateKey string) map[string]interface{} {
	t.Helper()
	raw, err := db.GetKey(WorkflowStateKey(taskID, stateKey))
	require.NoError(t, err, "expected wfstate key %s to exist", stateKey)
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

// TestWorkflowStateBinding_SetGetListPersist covers the {{state.*}} binding:
// set/get/list within a run, real persistence under wfstate:<taskId>, and — with a
// fresh VM/processor on the same db+taskId — cross-run persistence.
func TestWorkflowStateBinding_SetGetListPersist(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()
	const taskID = "state-test-task"

	// Run 1: set two keys, then probe get + list by writing the results back so we
	// can assert them via the DB (avoids parsing structpb output).
	runCustomCodeForState(t, db, taskID, `
		state.set("ntfy:a", { x: 1 });
		state.set("seen:b", { y: 2 });
		var got = state.get("ntfy:a");
		var keys = state.list("ntfy:");
		state.set("_probe", { gotX: got && got.x, keyCount: keys.length, firstKey: keys[0] });
		return true;
	`, false)

	// Values persisted under wfstate:<taskID>:<stateKey>.
	require.Equal(t, float64(1), readStateMap(t, db, taskID, "ntfy:a")["x"])
	require.Equal(t, float64(2), readStateMap(t, db, taskID, "seen:b")["y"])

	// The probe confirms get + list worked inside the JS: list("ntfy:") matched
	// exactly the one ntfy: key (not seen:b).
	probe := readStateMap(t, db, taskID, "_probe")
	require.Equal(t, float64(1), probe["gotX"])
	require.Equal(t, float64(1), probe["keyCount"])
	require.Equal(t, "ntfy:a", probe["firstKey"])

	// Run 2: a FRESH VM/processor on the same db + taskID still sees run-1 state.
	runCustomCodeForState(t, db, taskID, `
		var a = state.get("ntfy:a");
		state.set("_probe2", { persisted: a && a.x });
		return true;
	`, false)
	require.Equal(t, float64(1), readStateMap(t, db, taskID, "_probe2")["persisted"])
}

// TestWorkflowStateBinding_SimulationDoesNotPersist: in simulation, writes go to an
// in-memory scratch, never the real DB — so a nodes:run / simulate preview can't
// mutate a live workflow's state.
func TestWorkflowStateBinding_SimulationDoesNotPersist(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()
	const taskID = "state-sim-task"

	// Read-after-write still works within the run (scratch), but nothing hits the DB.
	runCustomCodeForState(t, db, taskID, `
		state.set("ntfy:x", { v: 1 });
		if (!state.get("ntfy:x")) { throw new Error("scratch read-after-write should work"); }
		return true;
	`, true)

	_, err := db.GetKey(WorkflowStateKey(taskID, "ntfy:x"))
	require.Error(t, err, "simulation writes must not reach the DB")
}

// TestRestAuthProvider covers the options.auth.provider parsing that gates
// server-side GoPlus token injection.
func TestRestAuthProvider(t *testing.T) {
	mk := func(opts map[string]interface{}) *avsproto.RestAPINode {
		var o *structpb.Value
		if opts != nil {
			v, err := structpb.NewValue(opts)
			require.NoError(t, err)
			o = v
		}
		return &avsproto.RestAPINode{Config: &avsproto.RestAPINode_Config{Options: o}}
	}

	require.Equal(t, "goplus", restAuthProvider(mk(map[string]interface{}{
		"auth": map[string]interface{}{"provider": "goplus"},
	})))
	// Trimmed + lower-cased.
	require.Equal(t, "goplus", restAuthProvider(mk(map[string]interface{}{
		"auth": map[string]interface{}{"provider": "  GoPlus "},
	})))
	// No auth key.
	require.Equal(t, "", restAuthProvider(mk(map[string]interface{}{"summarize": true})))
	// No options / no config.
	require.Equal(t, "", restAuthProvider(mk(nil)))
	require.Equal(t, "", restAuthProvider(&avsproto.RestAPINode{}))
	require.Equal(t, "", restAuthProvider(nil))
}

// TestRestGoPlusAuthInjection verifies the Authorization header is attached when
// options.auth.provider="goplus" and a token is available, and left unset for the
// keyless fallback. The token source (goplusTokenProvider) is stubbed so the test
// never hits the real GoPlus endpoint.
func TestRestGoPlusAuthInjection(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":1,"message":"ok","result":[]}`))
	}))
	defer srv.Close()

	orig := goplusTokenProvider
	defer func() { goplusTokenProvider = orig }()

	authNode := func() *avsproto.RestAPINode {
		opts, err := structpb.NewValue(map[string]interface{}{"auth": map[string]interface{}{"provider": "goplus"}})
		require.NoError(t, err)
		return &avsproto.RestAPINode{Config: &avsproto.RestAPINode_Config{Url: srv.URL, Method: "GET", Options: opts}}
	}
	run := func(node *avsproto.RestAPINode) {
		gotAuth = ""
		taskNode := &avsproto.TaskNode{Id: "r", Name: "r", TaskType: &avsproto.TaskNode_RestApi{RestApi: node}}
		vm, err := NewVMWithData(&model.Workflow{Task: &avsproto.Task{
			Id:      "auth-test",
			Nodes:   []*avsproto.TaskNode{taskNode},
			Edges:   []*avsproto.TaskEdge{{Id: "e1", Source: "trigger", Target: "r"}},
			Trigger: &avsproto.TaskTrigger{Id: "trigger", Name: "trigger"},
		}}, nil, testutil.GetTestSmartWalletConfig(), nil)
		require.NoError(t, err)
		proc := NewRestProcessor(vm)
		_, err = proc.Execute("r", node)
		require.NoError(t, err)
	}

	// A token from the provider is attached as the Authorization header.
	goplusTokenProvider = func() string { return "Bearer test-token" }
	run(authNode())
	require.Equal(t, "Bearer test-token", gotAuth)

	// Keyless fallback (provider returns "") sends no Authorization header.
	goplusTokenProvider = func() string { return "" }
	run(authNode())
	require.Equal(t, "", gotAuth)
}
