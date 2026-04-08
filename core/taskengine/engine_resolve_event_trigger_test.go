package taskengine

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestResolveEventTriggerTemplates_TopicAddress verifies that {{settings.runner}}
// is substituted into a Transfer-event topic[2] before the task is persisted.
// Regression for the bug where CreateTask shipped the literal "{{settings.runner}}"
// to the operator, causing the topic filter to degrade to a wildcard and the
// task to fire on unrelated events.
func TestResolveEventTriggerTemplates_TopicAddress(t *testing.T) {
	const transferSig = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
	const runner = "0x6cF121b8783Ae78A30A46DD4Ae1609E436422C26"

	settings, err := structpb.NewValue(map[string]interface{}{
		"runner": runner,
	})
	if err != nil {
		t.Fatalf("structpb.NewValue: %v", err)
	}

	trigger := &avsproto.TaskTrigger{
		TriggerType: &avsproto.TaskTrigger_Event{
			Event: &avsproto.EventTrigger{
				Config: &avsproto.EventTrigger_Config{
					Queries: []*avsproto.EventTrigger_Query{
						{
							Addresses: []string{"0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"},
							Topics: []string{
								transferSig,
								"", // from = wildcard
								"{{settings.runner}}",
							},
						},
					},
				},
			},
		},
	}

	inputVars := map[string]*structpb.Value{"settings": settings}

	if err := resolveEventTriggerTemplates(trigger, inputVars, nil); err != nil {
		t.Fatalf("resolveEventTriggerTemplates: %v", err)
	}

	got := trigger.GetEvent().GetConfig().GetQueries()[0].Topics[2]
	if got != runner {
		t.Errorf("topic[2] not resolved: got %q, want %q", got, runner)
	}

	// topic[0] (signature) and topic[1] (empty) should be untouched.
	if trigger.GetEvent().GetConfig().GetQueries()[0].Topics[0] != transferSig {
		t.Errorf("topic[0] mutated unexpectedly")
	}
	if trigger.GetEvent().GetConfig().GetQueries()[0].Topics[1] != "" {
		t.Errorf("topic[1] mutated unexpectedly")
	}
}

// TestResolveEventTriggerTemplates_NoOpForNonEventTrigger verifies the helper
// is a safe no-op for triggers that aren't EventTriggers.
func TestResolveEventTriggerTemplates_NoOpForNonEventTrigger(t *testing.T) {
	trigger := &avsproto.TaskTrigger{
		TriggerType: &avsproto.TaskTrigger_Manual{
			Manual: &avsproto.ManualTrigger{},
		},
	}
	if err := resolveEventTriggerTemplates(trigger, nil, nil); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

// TestResolveEventTriggerTemplates_NilTrigger ensures nil input doesn't panic.
func TestResolveEventTriggerTemplates_NilTrigger(t *testing.T) {
	if err := resolveEventTriggerTemplates(nil, nil, nil); err != nil {
		t.Errorf("expected no error for nil trigger, got: %v", err)
	}
}
