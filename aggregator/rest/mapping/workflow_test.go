package mapping

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// TestOpenAPIToProtoCreateWorkflow_MirrorsNameIntoSettings asserts that the
// top-level CreateWorkflowRequest.name field is mirrored into
// inputVariables.settings.name before the request reaches the engine.
//
// Background: the engine's NewWorkflowFromProtobuf sources Task.Name
// exclusively from settings["name"] (a v3 carryover where Task had a
// top-level Name field but persistence went through the free-form
// input_variables map). Without this mirror, callers who pass only the
// documented top-level body.name get a workflow with an empty name —
// silently, no warning.
func TestOpenAPIToProtoCreateWorkflow_MirrorsNameIntoSettings(t *testing.T) {
	t.Run("body.name populates settings.name when settings has no name", func(t *testing.T) {
		name := "Recurring payment with report"
		in := generated.CreateWorkflowRequest{
			Name: &name,
			InputVariables: &generated.InputVariables{
				"settings": map[string]interface{}{
					"runner": "0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557",
				},
			},
			Trigger: cronTrigger(),
			Nodes:   []generated.Node{customCodeNode()},
			Edges:   &[]generated.Edge{{Id: "e1", Source: "trigger", Target: "node1"}},
		}

		out, err := OpenAPIToProtoCreateWorkflow(in)
		require.NoError(t, err)
		require.NotNil(t, out.InputVariables["settings"])

		settings := out.InputVariables["settings"].GetStructValue().AsMap()
		assert.Equal(t, name, settings["name"])
		assert.Equal(t, "0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557", settings["runner"])
	})

	t.Run("body.name overrides settings.name when both are set and differ", func(t *testing.T) {
		bodyName := "Top-level wins"
		in := generated.CreateWorkflowRequest{
			Name: &bodyName,
			InputVariables: &generated.InputVariables{
				"settings": map[string]interface{}{
					"name":   "Settings loses",
					"runner": "0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557",
				},
			},
			Trigger: cronTrigger(),
			Nodes:   []generated.Node{customCodeNode()},
			Edges:   &[]generated.Edge{{Id: "e1", Source: "trigger", Target: "node1"}},
		}

		out, err := OpenAPIToProtoCreateWorkflow(in)
		require.NoError(t, err)

		settings := out.InputVariables["settings"].GetStructValue().AsMap()
		assert.Equal(t, bodyName, settings["name"], "body.name should win when both are set")
	})

	t.Run("settings.name is preserved when body.name is absent", func(t *testing.T) {
		in := generated.CreateWorkflowRequest{
			// no Name field
			InputVariables: &generated.InputVariables{
				"settings": map[string]interface{}{
					"name":   "Only settings has it",
					"runner": "0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557",
				},
			},
			Trigger: cronTrigger(),
			Nodes:   []generated.Node{customCodeNode()},
			Edges:   &[]generated.Edge{{Id: "e1", Source: "trigger", Target: "node1"}},
		}

		out, err := OpenAPIToProtoCreateWorkflow(in)
		require.NoError(t, err)

		settings := out.InputVariables["settings"].GetStructValue().AsMap()
		assert.Equal(t, "Only settings has it", settings["name"])
	})

	t.Run("empty body.name leaves settings unchanged", func(t *testing.T) {
		empty := ""
		in := generated.CreateWorkflowRequest{
			Name: &empty,
			InputVariables: &generated.InputVariables{
				"settings": map[string]interface{}{
					"name":   "Keep me",
					"runner": "0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557",
				},
			},
			Trigger: cronTrigger(),
			Nodes:   []generated.Node{customCodeNode()},
			Edges:   &[]generated.Edge{{Id: "e1", Source: "trigger", Target: "node1"}},
		}

		out, err := OpenAPIToProtoCreateWorkflow(in)
		require.NoError(t, err)

		settings := out.InputVariables["settings"].GetStructValue().AsMap()
		assert.Equal(t, "Keep me", settings["name"], "empty body.name should not clobber settings.name")
	})

	t.Run("body.name + no inputVariables creates settings on the fly", func(t *testing.T) {
		// This case can't actually reach the engine in production —
		// the engine still requires settings.runner — but the mapper
		// should produce well-formed proto either way.
		name := "Bootstrap"
		in := generated.CreateWorkflowRequest{
			Name:    &name,
			Trigger: cronTrigger(),
			Nodes:   []generated.Node{customCodeNode()},
			Edges:   &[]generated.Edge{{Id: "e1", Source: "trigger", Target: "node1"}},
		}

		out, err := OpenAPIToProtoCreateWorkflow(in)
		require.NoError(t, err)
		require.NotNil(t, out.InputVariables["settings"])

		settings := out.InputVariables["settings"].GetStructValue().AsMap()
		assert.Equal(t, name, settings["name"])
	})
}

// cronTrigger / customCodeNode — minimal valid fixtures for the
// regression test. Keep them inline so the test file is self-contained
// and doesn't depend on whatever the existing node_test.go fixtures
// happen to be.
func cronTrigger() generated.Trigger {
	id := "trigger"
	typ := generated.Cron
	inner := generated.CronTrigger{
		Type:   &typ,
		Config: &generated.CronTriggerConfig{Schedules: []string{"0 * * * *"}},
	}
	t := generated.Trigger{Id: &id, Type: generated.TriggerTypeCron, Name: "cron"}
	_ = t.FromCronTrigger(inner)
	return t
}

func customCodeNode() generated.Node {
	name := "node1"
	typ := generated.CustomCode
	inner := generated.CustomCodeNode{
		Type: &typ,
		Config: &generated.CustomCodeNodeConfig{
			Lang:   generated.Lang("javascript"),
			Source: "return {ok: true};",
		},
	}
	n := generated.Node{Id: "node1", Type: generated.NodeTypeCustomCode, Name: &name}
	_ = n.FromCustomCodeNode(inner)
	return n
}

// TestProtoToOpenAPIWorkflow_ExecutionBudget covers the two fields a client
// needs to answer "how much life does this workflow have left, and why did it
// stop?" — neither of which was previously derivable from the response.
//
// `remainingExecutions` is computed server-side rather than left to the client:
// `executionCount` is omitted when zero, so a client subtracting the two fields
// on a never-run workflow has to know that an absent field means 0.
//
// `completionReason` exists because an exhausted budget and a passed expiry
// both produce status `completed`. Before this, they were indistinguishable.
func TestProtoToOpenAPIWorkflow_ExecutionBudget(t *testing.T) {
	baseTask := func() *avsproto.Task {
		return &avsproto.Task{
			Id:                 "01kxrxyb9x35phtw194afsz563",
			Owner:              "0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557",
			SmartWalletAddress: "0x8Ee38eB323c14a1752DABDA1cca9661AEE377017",
			Status:             avsproto.TaskStatus_Enabled,
			MaxExecution:       100,
			Trigger: &avsproto.TaskTrigger{
				Id:          "trigger",
				Name:        "tick",
				Type:        avsproto.TriggerType_TRIGGER_TYPE_CRON,
				TriggerType: &avsproto.TaskTrigger_Cron{Cron: &avsproto.CronTrigger{Config: &avsproto.CronTrigger_Config{Schedules: []string{"0 */6 * * *"}}}},
			},
		}
	}

	t.Run("remaining is max minus count", func(t *testing.T) {
		task := baseTask()
		task.ExecutionCount = 30
		out, err := ProtoToOpenAPIWorkflow(task)
		require.NoError(t, err)
		require.NotNil(t, out.RemainingExecutions)
		assert.Equal(t, int64(70), *out.RemainingExecutions)
	})

	t.Run("remaining is reported on a never-run workflow", func(t *testing.T) {
		// executionCount is omitted at 0, so this is precisely the case a
		// client-side subtraction gets wrong.
		out, err := ProtoToOpenAPIWorkflow(baseTask())
		require.NoError(t, err)
		require.Nil(t, out.ExecutionCount, "guard the premise: count is omitted at zero")
		require.NotNil(t, out.RemainingExecutions)
		assert.Equal(t, int64(100), *out.RemainingExecutions)
	})

	t.Run("remaining is emitted as zero, not omitted, once exhausted", func(t *testing.T) {
		task := baseTask()
		task.ExecutionCount = 100
		task.Status = avsproto.TaskStatus_Completed
		out, err := ProtoToOpenAPIWorkflow(task)
		require.NoError(t, err)
		require.NotNil(t, out.RemainingExecutions, "zero is the whole point; omitting it reads as 'not reported'")
		assert.Equal(t, int64(0), *out.RemainingExecutions)
	})

	t.Run("remaining never goes negative", func(t *testing.T) {
		task := baseTask()
		task.ExecutionCount = 105 // a suspended run can overshoot
		out, err := ProtoToOpenAPIWorkflow(task)
		require.NoError(t, err)
		require.NotNil(t, out.RemainingExecutions)
		assert.Equal(t, int64(0), *out.RemainingExecutions)
	})

	t.Run("completion reason distinguishes exhaustion from expiry", func(t *testing.T) {
		for _, tc := range []struct {
			reason avsproto.TaskCompletionReason
			want   generated.WorkflowCompletionReason
		}{
			{avsproto.TaskCompletionReason_TASK_COMPLETION_REASON_MAX_EXECUTIONS_REACHED, generated.TASKCOMPLETIONREASONMAXEXECUTIONSREACHED},
			{avsproto.TaskCompletionReason_TASK_COMPLETION_REASON_EXPIRED, generated.TASKCOMPLETIONREASONEXPIRED},
		} {
			task := baseTask()
			task.Status = avsproto.TaskStatus_Completed
			task.CompletionReason = tc.reason
			out, err := ProtoToOpenAPIWorkflow(task)
			require.NoError(t, err)
			require.NotNil(t, out.CompletionReason, "reason %v should be surfaced", tc.reason)
			assert.Equal(t, tc.want, *out.CompletionReason)
		}
	})

	t.Run("unspecified reason is omitted rather than serialized", func(t *testing.T) {
		out, err := ProtoToOpenAPIWorkflow(baseTask())
		require.NoError(t, err)
		assert.Nil(t, out.CompletionReason, "UNSPECIFIED carries no information")
	})
}
