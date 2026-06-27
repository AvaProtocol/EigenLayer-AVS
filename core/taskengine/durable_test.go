package taskengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestWakeSubscription_Validate(t *testing.T) {
	cases := []struct {
		name string
		sub  WakeSubscription
		ok   bool
	}{
		{
			name: "chain event valid",
			sub:  WakeSubscription{Kind: WakeChainEvent, ChainEvent: &avsproto.EventTrigger_Config{ChainId: 84532}, TimeoutAt: 1},
			ok:   true,
		},
		{
			name: "chain event missing config",
			sub:  WakeSubscription{Kind: WakeChainEvent, TimeoutAt: 1},
			ok:   false,
		},
		{
			name: "external valid",
			sub:  WakeSubscription{Kind: WakeExternalSignal, External: &ExternalSignalSpec{Channel: "telegram"}, TimeoutAt: 1},
			ok:   true,
		},
		{
			name: "external missing channel",
			sub:  WakeSubscription{Kind: WakeExternalSignal, External: &ExternalSignalSpec{}, TimeoutAt: 1},
			ok:   false,
		},
		{
			name: "external must not set chain event",
			sub:  WakeSubscription{Kind: WakeExternalSignal, External: &ExternalSignalSpec{Channel: "api"}, ChainEvent: &avsproto.EventTrigger_Config{}, TimeoutAt: 1},
			ok:   false,
		},
		{
			name: "timer valid",
			sub:  WakeSubscription{Kind: WakeTimer, TimeoutAt: 1},
			ok:   true,
		},
		{
			name: "every wait must be bounded",
			sub:  WakeSubscription{Kind: WakeTimer, TimeoutAt: 0},
			ok:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.sub.Validate()
			if tc.ok {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestSignal_Validate(t *testing.T) {
	require.Error(t, (&Signal{}).Validate(), "missing executionId")
	require.Error(t, (&Signal{ExecutionID: "e1", Kind: WakeExternalSignal}).Validate(), "approval needs a decision")
	require.NoError(t, (&Signal{ExecutionID: "e1", Kind: WakeChainEvent}).Validate())
	require.NoError(t, (&Signal{ExecutionID: "e1", Kind: WakeExternalSignal, Decision: "approve", Approver: "0xowner"}).Validate())
}

func TestEnumStrings(t *testing.T) {
	assert.Equal(t, "suspend", RunSuspend.String())
	assert.Equal(t, "chain_event", WakeChainEvent.String())
	assert.Equal(t, "external_signal", WakeExternalSignal.String())
	assert.Equal(t, "timer", WakeTimer.String())
}
