package mapping

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
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
