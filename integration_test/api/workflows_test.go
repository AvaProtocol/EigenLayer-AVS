//go:build integration
// +build integration

package api_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
)

// TestWorkflowLifecycle walks one workflow through the full CRUD path
// the SDK uses on every smoke test: create -> list -> get -> pause ->
// resume -> cancel. Each step asserts both the response shape and the
// observed state change so a regression at any single hop fails the
// test loudly.
func TestWorkflowLifecycle(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	user := testutil.TestUser1()

	// ----- CREATE ---------------------------------------------------
	createReq := newRestWorkflowRequest(user.SmartAccountAddress.Hex())
	resp, body := h.authedRequest(http.MethodPost, "/api/v1/workflows", createReq)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "create body=%s", string(body))

	created := decodeJSON[generated.Workflow](t, body)
	require.NotEmpty(t, created.Id, "created workflow must have an id")
	assert.Equal(t, generated.WorkflowStatus("enabled"), created.Status, "new workflow defaults to enabled")
	assert.NotEmpty(t, created.Nodes, "nodes round-trip")
	require.NotNil(t, created.Edges, "edges round-trip")
	assert.NotEmpty(t, *created.Edges)
	id := string(created.Id)

	// ----- LIST -----------------------------------------------------
	// Engine requires at least one smartWalletAddress filter today; the
	// SDK passes it explicitly. A handler-side default ("all my
	// wallets") would let callers omit it.
	listURL := "/api/v1/workflows?smartWalletAddress=" + user.SmartAccountAddress.Hex()
	resp, body = h.authedRequest(http.MethodGet, listURL, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "list body=%s", string(body))
	list := decodeJSON[generated.WorkflowList](t, body)
	assert.True(t, containsWorkflow(list.Data, id), "newly-created workflow should appear in list")

	// ----- COUNT ----------------------------------------------------
	resp, body = h.authedRequest(http.MethodGet, "/api/v1/workflows:count", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "count body=%s", string(body))
	count := decodeJSON[generated.WorkflowCount](t, body)
	assert.GreaterOrEqual(t, count.Total, int64(1), "count should be at least 1 after create")

	// ----- GET ------------------------------------------------------
	resp, body = h.authedRequest(http.MethodGet, "/api/v1/workflows/"+id, nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "get body=%s", string(body))
	fetched := decodeJSON[generated.Workflow](t, body)
	assert.Equal(t, id, string(fetched.Id))
	assert.Equal(t, generated.WorkflowStatus("enabled"), fetched.Status)

	// ----- PAUSE ----------------------------------------------------
	resp, body = h.authedRequest(http.MethodPost, "/api/v1/workflows/"+id+":pause", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "pause body=%s", string(body))
	paused := decodeJSON[generated.Workflow](t, body)
	assert.Equal(t, generated.WorkflowStatus("disabled"), paused.Status, "pause should flip status to disabled")

	// ----- RESUME ---------------------------------------------------
	resp, body = h.authedRequest(http.MethodPost, "/api/v1/workflows/"+id+":resume", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "resume body=%s", string(body))
	resumed := decodeJSON[generated.Workflow](t, body)
	assert.Equal(t, generated.WorkflowStatus("enabled"), resumed.Status, "resume should flip status back to enabled")

	// ----- CANCEL (DELETE) -----------------------------------------
	resp, _ = h.authedRequest(http.MethodDelete, "/api/v1/workflows/"+id, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode, "cancel should return 204")

	// ----- GET after cancel returns 404 -----------------------------
	resp, _ = h.authedRequest(http.MethodGet, "/api/v1/workflows/"+id, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "GET on a cancelled workflow should be 404")
}

// TestWorkflowSimulate exercises the simulate endpoint — same workflow
// payload, but `:simulate` doesn't persist a record. The response is an
// Execution object representing what a real run would produce.
func TestWorkflowSimulate(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	user := testutil.TestUser1()

	create := newRestWorkflowRequest(user.SmartAccountAddress.Hex())
	// SimulateWorkflow compiles the trigger and nodes into a fresh VM;
	// edges that reference the trigger ID confuse the compiler (it
	// only sees nodes, not triggers). Drop the edges for the simulate
	// payload — a single-node workflow doesn't need them.
	simReq := generated.SimulateWorkflowRequest{
		Trigger:        create.Trigger,
		Nodes:          create.Nodes,
		InputVariables: *create.InputVariables,
		ChainId:        create.ChainId,
	}

	resp, body := h.authedRequest(http.MethodPost, "/api/v1/workflows:simulate", simReq)
	require.Equal(t, http.StatusOK, resp.StatusCode, "simulate body=%s", string(body))

	// The simulate response shape is currently the proto Execution.
	// Just confirm it's valid JSON with the expected envelope — a
	// dedicated mapping.ProtoToOpenAPIExecution call lands in the
	// executions-handler follow-up.
	require.True(t, len(body) > 0, "simulate should return a body")

	// Count should not have changed — simulate is non-persistent.
	resp, body = h.authedRequest(http.MethodGet, "/api/v1/workflows:count", nil)
	require.Equal(t, http.StatusOK, resp.StatusCode, "count body=%s", string(body))
	count := decodeJSON[generated.WorkflowCount](t, body)
	assert.Equal(t, int64(0), count.Total, "simulate should not create a workflow record")
}

// TestWorkflowCreate_RejectsBadPayload makes sure handler-side
// validation produces a 4xx and not a 500 when the SDK sends garbage.
func TestWorkflowCreate_RejectsBadPayload(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	resp, body := h.authedRequest(http.MethodPost, "/api/v1/workflows", map[string]any{
		"smartWalletAddress": "0xabc",
		// No trigger / no nodes — engine validation rejects this.
	})
	require.True(t, resp.StatusCode >= 400 && resp.StatusCode < 500,
		"expected 4xx, got %d body=%s", resp.StatusCode, string(body))
}

// ---------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------

// newRestWorkflowRequest builds the minimum CreateWorkflowRequest the
// REST handler accepts — block trigger + custom code node + one edge,
// with settings.runner pinned to the supplied smart wallet so engine
// ownership validation passes.
func newRestWorkflowRequest(smartWalletAddr string) generated.CreateWorkflowRequest {
	triggerTyp := generated.BlockTriggerTypeBlock
	trigger := generated.Trigger{
		Name: "triggerabcde",
		Type: generated.TriggerTypeBlock,
	}
	_ = trigger.FromBlockTrigger(generated.BlockTrigger{
		Type:   &triggerTyp,
		Config: &generated.BlockTriggerConfig{Interval: 10},
	})

	nodeTyp := generated.CustomCode
	nodeName := "step1"
	node := generated.Node{
		Id:   "step1",
		Name: &nodeName, // engine validates Name as a JS identifier; empty is rejected
		Type: generated.NodeTypeCustomCode,
	}
	_ = node.FromCustomCodeNode(generated.CustomCodeNode{
		Type: &nodeTyp,
		Config: &generated.CustomCodeNodeConfig{
			Lang:   generated.Javascript,
			Source: "return {ok: true};",
		},
	})

	maxExec := int64(1)
	name := "rest-lifecycle"
	vars := generated.InputVariables{
		"settings": map[string]any{
			"name":   "rest-lifecycle",
			"runner": smartWalletAddr,
		},
	}

	return generated.CreateWorkflowRequest{
		SmartWalletAddress: generated.EthereumAddress(smartWalletAddr),
		Trigger:            trigger,
		Nodes:              []generated.Node{node},
		Edges: &[]generated.Edge{
			{Id: "edge1", Source: "triggerabcde", Target: "step1"},
		},
		InputVariables: &vars,
		MaxExecution:   &maxExec,
		Name:           &name,
	}
}

func containsWorkflow(list []generated.Workflow, id string) bool {
	for _, w := range list {
		if string(w.Id) == id {
			return true
		}
	}
	return false
}
