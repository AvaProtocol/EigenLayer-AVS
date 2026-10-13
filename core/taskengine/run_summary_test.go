package taskengine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestStudioNotifyKeepsStructuredSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok": true,
			"summary": map[string]interface{}{
				"subject":       "Batch pay ran",
				"promptVersion": "v9",
				"body": map[string]interface{}{
					"summary":       "Sent 0.25 USDC on Base",
					"notice":        "",
					"executedSteps": 2,
					"totalSteps":    3,
					"skippedSteps":  0,
					"executions": []map[string]interface{}{
						{"description": "Sent 0.1 USDC to 0xc60e…C788", "txHash": "0x5de5"},
						{"description": "Sent 0.15 USDC to lunax.base.eth", "txHash": "0xa492"},
					},
					"errors": []string{},
				},
			},
		})
	}))
	defer server.Close()

	step := runNotifyStep(t, server.URL+"/api/notify", true)
	data := step.GetRestApi().GetData().AsInterface().(map[string]interface{})
	kept := data["data"].(map[string]interface{})
	require.Equal(t, "Batch pay ran", kept["subject"])
	_, hasPrompt := kept["promptVersion"]
	require.False(t, hasPrompt)

	body := kept["body"].(map[string]interface{})
	require.Equal(t, "Sent 0.25 USDC on Base", body["summary"])
	executions := body["executions"].([]interface{})
	require.Len(t, executions, 2)
	require.Equal(t, "0xa492", executions[1].(map[string]interface{})["txHash"])

	execution := &avsproto.Execution{Steps: []*avsproto.Execution_Step{step}}
	attachRunSummary(execution)
	require.NotNil(t, execution.GetSummary())
	summary := execution.GetSummary().AsInterface().(map[string]interface{})
	require.Equal(t, "Batch pay ran", summary["subject"])
	require.Len(t, summary["body"].(map[string]interface{})["executions"].([]interface{}), 2)
}

func TestStudioNotifyWithoutSummarizeLeavesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok": true,
			"summary": map[string]interface{}{
				"subject": "should stay nested",
				"body":    map[string]interface{}{"summary": "line"},
			},
		})
	}))
	defer server.Close()

	step := runNotifyStep(t, server.URL+"/api/notify", false)
	data := step.GetRestApi().GetData().AsInterface().(map[string]interface{})
	raw := data["data"].(map[string]interface{})
	require.Equal(t, true, raw["ok"])
	require.Contains(t, raw, "summary")

	execution := &avsproto.Execution{Steps: []*avsproto.Execution_Step{step}}
	attachRunSummary(execution)
	require.Nil(t, execution.GetSummary())
}

func TestAttachRunSummaryUsesLastSummaryStep(t *testing.T) {
	plain, err := structpb.NewValue(map[string]interface{}{
		"data": map[string]interface{}{"ok": true},
	})
	require.NoError(t, err)
	first := summaryStepValue(t, "first", "one")
	last := summaryStepValue(t, "last", "two")

	execution := &avsproto.Execution{Steps: []*avsproto.Execution_Step{
		{OutputData: &avsproto.Execution_Step_RestApi{RestApi: &avsproto.RestAPINode_Output{Data: plain}}},
		{OutputData: &avsproto.Execution_Step_RestApi{RestApi: &avsproto.RestAPINode_Output{Data: first}}},
		{OutputData: &avsproto.Execution_Step_RestApi{RestApi: &avsproto.RestAPINode_Output{Data: last}}},
	}}
	attachRunSummary(execution)
	got := execution.GetSummary().AsInterface().(map[string]interface{})
	require.Equal(t, "last", got["subject"])

	attachRunSummary(execution)
	require.Equal(t, "last", execution.GetSummary().AsInterface().(map[string]interface{})["subject"])
}

func summaryStepValue(t *testing.T, subject, line string) *structpb.Value {
	t.Helper()
	value, err := structpb.NewValue(map[string]interface{}{
		"status": float64(200),
		"data": map[string]interface{}{
			"subject": subject,
			"body": map[string]interface{}{
				"summary":       line,
				"executions":    []interface{}{},
				"executedSteps": float64(1),
			},
		},
	})
	require.NoError(t, err)
	return value
}

func runNotifyStep(t *testing.T, url string, summarize bool) *avsproto.Execution_Step {
	t.Helper()
	var options *structpb.Value
	if summarize {
		opts, err := structpb.NewValue(map[string]interface{}{"summarize": true})
		require.NoError(t, err)
		options = opts
	}
	node := &avsproto.RestAPINode{
		Config: &avsproto.RestAPINode_Config{
			Url:     url,
			Method:  "POST",
			Body:    `{}`,
			Options: options,
			Headers: map[string]string{"Content-Type": "application/json"},
		},
	}
	nodes := []*avsproto.TaskNode{{
		Id:   "notify",
		Name: "report",
		TaskType: &avsproto.TaskNode_RestApi{
			RestApi: node,
		},
	}}
	trigger := &avsproto.TaskTrigger{Id: "trigger", Name: "manual"}
	vm, err := NewVMWithData(&model.Workflow{
		Task: &avsproto.Task{
			Id:      "run-summary",
			Nodes:   nodes,
			Edges:   []*avsproto.TaskEdge{{Id: "e1", Source: trigger.Id, Target: "notify"}},
			Trigger: trigger,
		},
	}, nil, &config.SmartWalletConfig{ChainID: 8453}, nil)
	require.NoError(t, err)

	step, err := NewRestProcessor(vm).Execute("notify", node)
	require.NoError(t, err)
	require.True(t, step.GetSuccess(), step.GetError())
	return step
}
