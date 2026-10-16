package taskengine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func dependencyWrite(id string) *avsproto.TaskNode {
	return &avsproto.TaskNode{Id: id, Name: id, Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
		TaskType: &avsproto.TaskNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{
			Config: &avsproto.ContractWriteNode_Config{},
		}},
	}
}

func dependencyVM(t *testing.T, nodes []*avsproto.TaskNode, edges []*avsproto.TaskEdge) *VM {
	t.Helper()
	vm, err := NewVMWithData(&model.Workflow{Task: &avsproto.Task{
		Id: "write-dependency-test", Nodes: nodes, Edges: edges,
		Trigger: &avsproto.TaskTrigger{Id: "trigger", Name: "trigger", TriggerType: &avsproto.TaskTrigger_Manual{}},
	}}, nil, &config.SmartWalletConfig{}, nil)
	require.NoError(t, err)
	vm.WithLogger(testutil.GetLogger())
	require.NoError(t, vm.Compile())
	return vm
}

func dependencyStep(t *testing.T, vm *VM, id string) *avsproto.Execution_Step {
	t.Helper()
	for _, step := range vm.ExecutionLogs {
		if step.Id == id {
			return step
		}
	}
	t.Fatalf("missing execution step %s", id)
	return nil
}

func TestWriteDependenciesFailedApprovalStillReports(t *testing.T) {
	var reports atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reports.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "summary": map[string]any{
			"subject": "Approval failed", "body": map[string]any{"errors": []string{"approval failed"}},
		}})
	}))
	defer server.Close()
	options, err := structpb.NewValue(map[string]any{"summarize": true})
	require.NoError(t, err)
	native := &avsproto.TaskNode{Id: "transfer", Name: "transfer", Type: avsproto.NodeType_NODE_TYPE_ETH_TRANSFER,
		TaskType: &avsproto.TaskNode_EthTransfer{EthTransfer: &avsproto.ETHTransferNode{Config: &avsproto.ETHTransferNode_Config{}}}}
	loop := &avsproto.TaskNode{Id: "batch", Name: "batch", Type: avsproto.NodeType_NODE_TYPE_LOOP,
		TaskType: &avsproto.TaskNode_Loop{Loop: &avsproto.LoopNode{
			Config: &avsproto.LoopNode_Config{InputVariable: "{{helper.data}}"},
			Runner: &avsproto.LoopNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{Config: &avsproto.ContractWriteNode_Config{}}},
		}}}
	report := &avsproto.TaskNode{Id: "report", Name: "report", Type: avsproto.NodeType_NODE_TYPE_REST_API,
		TaskType: &avsproto.TaskNode_RestApi{RestApi: &avsproto.RestAPINode{Config: &avsproto.RestAPINode_Config{
			Url: server.URL + "/api/notify", Method: http.MethodPost, Body: `{}`, Options: options,
		}}}}
	nodes := []*avsproto.TaskNode{dependencyWrite("approve"), customCodeNode("helper"), dependencyWrite("supply"), native, loop, dependencyWrite("independent"), report}
	edges := []*avsproto.TaskEdge{
		{Source: "trigger", Target: "approve"}, {Source: "approve", Target: "helper"},
		{Source: "helper", Target: "supply"}, {Source: "supply", Target: "transfer"},
		{Source: "transfer", Target: "batch"}, {Source: "batch", Target: "report"},
		{Source: "trigger", Target: "independent"}, {Source: "independent", Target: "report"},
	}
	vm := dependencyVM(t, nodes, edges)
	require.NoError(t, vm.Run())
	approve := dependencyStep(t, vm, "approve")
	require.False(t, approve.Success)
	require.NotEmpty(t, approve.Error)
	for _, id := range []string{"supply", "transfer", "batch"} {
		step := dependencyStep(t, vm, id)
		require.False(t, step.Success)
		require.Contains(t, step.Error, approve.Error)
		require.Equal(t, true, step.Metadata.AsInterface().(map[string]any)["skipped"])
		require.Equal(t, "approve", step.Metadata.AsInterface().(map[string]any)["prerequisiteNodeId"])
		require.Empty(t, step.GasUsed, "a skipped write was never submitted")
	}
	require.True(t, dependencyStep(t, vm, "helper").Success, "off-chain successors still run")
	require.NotContains(t, dependencyStep(t, vm, "independent").Error, "Skipped", "an independent write is still attempted")
	require.True(t, dependencyStep(t, vm, "report").Success)
	require.EqualValues(t, 1, reports.Load(), "the terminal report still reaches notify")
	_, _, status := vm.AnalyzeExecutionResult()
	require.Equal(t, ExecutionFailed, status, "a successful report does not hide a failed write")
}

func TestWriteDependenciesRestoredOutcomes(t *testing.T) {
	for _, outcome := range []string{"confirmed", "reverted", "unresolved", "pending"} {
		t.Run(outcome, func(t *testing.T) {
			vm := dependencyVM(t, []*avsproto.TaskNode{dependencyWrite("approve"), customCodeNode("helper"), dependencyWrite("supply")}, []*avsproto.TaskEdge{
				{Source: "trigger", Target: "approve"}, {Source: "approve", Target: "helper"}, {Source: "helper", Target: "supply"},
			})
			// Restore a completed prefix, as a durable execution does; no runner
			// output variables or simulated allowance are injected into the VM.
			prefix := &avsproto.Execution_Step{Id: "approve", Name: "approve", Success: outcome == "confirmed" || outcome == "pending"}
			if outcome == "pending" || outcome == "unresolved" {
				prefix.Metadata, _ = structpb.NewValue([]any{map[string]any{"receipt": map[string]any{"status": "pending", "userOpHash": "0xpending"}}})
			}
			if outcome == "reverted" {
				prefix.Error = "execution reverted: approval refused"
			}
			if outcome == "unresolved" {
				prefix.Error = "UserOperation confirmation unresolved; check its status before retrying"
			}
			vm.ExecutionLogs = []*avsproto.Execution_Step{prefix}
			vm.resumeCompleted = map[string]bool{"approve": true}
			require.NoError(t, vm.Run())
			step := dependencyStep(t, vm, "supply")
			if outcome == "confirmed" {
				require.NotContains(t, step.Error, "Skipped", "a confirmed prerequisite permits submission")
			} else {
				require.Contains(t, step.Error, "Skipped: prerequisite write")
			}
			require.Same(t, prefix, vm.ExecutionLogs[0], "the original receipt/outcome is preserved")
		})
	}
}

func TestWriteDependenciesNonWriteFailureDoesNotBlockWrite(t *testing.T) {
	helper := customCodeNode("helper")
	helper.GetCustomCode().Config.Source = `throw new Error("read-side failure");`
	vm := dependencyVM(t, []*avsproto.TaskNode{helper, dependencyWrite("supply")}, []*avsproto.TaskEdge{
		{Source: "trigger", Target: "helper"}, {Source: "helper", Target: "supply"},
	})
	require.NoError(t, vm.Run())
	require.False(t, dependencyStep(t, vm, "helper").Success)
	require.NotContains(t, dependencyStep(t, vm, "supply").Error, "Skipped")
}

func TestWriteDependenciesCrossBranchCondition(t *testing.T) {
	branch := &avsproto.TaskNode{Id: "branch", Name: "branch", Type: avsproto.NodeType_NODE_TYPE_BRANCH,
		TaskType: &avsproto.TaskNode_Branch{Branch: &avsproto.BranchNode{Config: &avsproto.BranchNode_Config{
			Conditions: []*avsproto.BranchNode_Condition{{Id: "0", Type: "if", Expression: "true"}},
		}}}}
	vm := dependencyVM(t, []*avsproto.TaskNode{dependencyWrite("approve"), branch, dependencyWrite("supply"), customCodeNode("report")}, []*avsproto.TaskEdge{
		{Source: "trigger", Target: "approve"}, {Source: "approve", Target: "branch"},
		{Source: "branch.0", Target: "supply"}, {Source: "supply", Target: "report"},
	})
	require.NoError(t, vm.Run())
	require.Contains(t, dependencyStep(t, vm, "supply").Error, "Skipped: prerequisite write")
	require.True(t, dependencyStep(t, vm, "report").Success)
}

func TestWriteDependenciesFailedLoopBlocksSuccessor(t *testing.T) {
	items := customCodeNode("items")
	items.GetCustomCode().Config.Source = "return [1];"
	batch := &avsproto.TaskNode{Id: "batch", Name: "batch", Type: avsproto.NodeType_NODE_TYPE_LOOP,
		TaskType: &avsproto.TaskNode_Loop{Loop: &avsproto.LoopNode{
			Config: &avsproto.LoopNode_Config{InputVariable: "{{items.data}}", IterVal: "item", IterKey: "index"},
			Runner: &avsproto.LoopNode_ContractWrite{ContractWrite: &avsproto.ContractWriteNode{Config: &avsproto.ContractWriteNode_Config{}}},
		}}}
	vm := dependencyVM(t, []*avsproto.TaskNode{items, batch, dependencyWrite("supply"), customCodeNode("report")}, []*avsproto.TaskEdge{
		{Source: "trigger", Target: "items"}, {Source: "items", Target: "batch"},
		{Source: "batch", Target: "supply"}, {Source: "supply", Target: "report"},
	})
	require.NoError(t, vm.Run())
	require.False(t, dependencyStep(t, vm, "batch").Success)
	require.Equal(t, "batch", dependencyStep(t, vm, "supply").Metadata.AsInterface().(map[string]any)["prerequisiteNodeId"])
	require.True(t, dependencyStep(t, vm, "report").Success)
}
