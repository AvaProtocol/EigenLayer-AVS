package model

import (
	"testing"

	"github.com/stretchr/testify/require"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// A stored address-less watch stays valid. CreateTask rejects new ones, but
// ValidateWithError runs on every boot and must not fail tasks already saved.
func TestValidateWithError_EventQueryWithoutAddress(t *testing.T) {
	task := &Workflow{Task: &avsproto.Task{
		Trigger: &avsproto.TaskTrigger{
			TriggerType: &avsproto.TaskTrigger_Event{
				Event: &avsproto.EventTrigger{
					Config: &avsproto.EventTrigger_Config{
						Queries: []*avsproto.EventTrigger_Query{{
							Addresses: nil,
							Topics:    []string{"0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"},
						}},
					},
				},
			},
		},
	}}

	require.NoError(t, task.ValidateWithError())
}
