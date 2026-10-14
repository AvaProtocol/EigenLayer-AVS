package taskengine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

const studioNotifyURL = "https://studio.example/api/notify"

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
	first := summaryStep(t, studioNotifyURL, "first", "one", true)
	last := summaryStep(t, studioNotifyURL, "last", "two", true)
	// Later replies with the same shape are not the summary step.
	unmarked := summaryStep(t, studioNotifyURL, "unmarked", "no", false)
	otherHost := summaryStep(t, "https://example.com/api/messages", "other-host", "no", true)

	execution := &avsproto.Execution{Steps: []*avsproto.Execution_Step{
		{OutputData: &avsproto.Execution_Step_RestApi{RestApi: &avsproto.RestAPINode_Output{Data: plain}}},
		first,
		last,
		unmarked,
		otherHost,
	}}
	attachRunSummary(execution)
	got := execution.GetSummary().AsInterface().(map[string]interface{})
	require.Equal(t, "last", got["subject"])

	attachRunSummary(execution)
	require.Equal(t, "last", execution.GetSummary().AsInterface().(map[string]interface{})["subject"])
}

func TestAttachRunSummaryIgnoresUnmarkedRestPayload(t *testing.T) {
	execution := &avsproto.Execution{Steps: []*avsproto.Execution_Step{
		summaryStep(t, studioNotifyURL, "Inbox", "you have mail", false),
		summaryStep(t, "https://example.com/api/messages", "Report", "sent", true),
	}}
	attachRunSummary(execution)
	require.Nil(t, execution.GetSummary())
}

func TestExpireWaitAttachesRunSummary(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()
	executor := &WorkflowExecutor{db: db, logger: testutil.GetLogger(), smartWalletConfig: &config.SmartWalletConfig{}}

	task := &model.Workflow{Task: &avsproto.Task{Id: "summary-expire"}}
	const execID = "exec-summary-expire"
	execution := &avsproto.Execution{
		Id:     execID,
		Status: avsproto.ExecutionStatus_EXECUTION_STATUS_WAITING,
		Steps: []*avsproto.Execution_Step{
			summaryStep(t, studioNotifyURL, "Batch pay ran", "Sent 0.25 USDC", true),
			summaryStep(t, "https://example.com/v1/messages", "not the report", "hello", false),
		},
	}
	raw, err := protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}.Marshal(execution)
	require.NoError(t, err)
	require.NoError(t, db.Set(TaskExecutionKey(task, execID), raw))

	out, err := executor.ExpireWait(task, execID)
	require.NoError(t, err)
	require.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_FAILED, out.Status)
	require.NotNil(t, out.GetSummary())
	require.Equal(t, "Batch pay ran", out.GetSummary().AsInterface().(map[string]interface{})["subject"])

	// A second expiry is a no-op and still returns the summary already stored.
	again, err := executor.ExpireWait(task, execID)
	require.NoError(t, err)
	require.Equal(t, "Batch pay ran", again.GetSummary().AsInterface().(map[string]interface{})["subject"])
}

func summaryStep(t *testing.T, requestURL, subject, line string, summarize bool) *avsproto.Execution_Step {
	t.Helper()
	value, err := structpb.NewValue(map[string]interface{}{
		"status": float64(200),
		"url":    requestURL,
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
	step := &avsproto.Execution_Step{
		OutputData: &avsproto.Execution_Step_RestApi{RestApi: &avsproto.RestAPINode_Output{Data: value}},
	}
	if summarize {
		configValue, cfgErr := structpb.NewValue(map[string]interface{}{
			"url":     requestURL,
			"method":  "POST",
			"options": map[string]interface{}{"summarize": true},
		})
		require.NoError(t, cfgErr)
		step.Config = configValue
	}
	return step
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
