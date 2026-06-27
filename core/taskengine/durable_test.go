package taskengine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
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

// TestWakeRegistry_RoundTrip proves the durable wake registry: persist (chain +
// external), scan/load (the boot re-arm entrypoint) with faithful round-trip
// including the proto chain-event, and GC. Restart durability rides on this.
func TestWakeRegistry_RoundTrip(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()

	chainSub := &WakeSubscription{
		Kind: WakeChainEvent,
		ChainEvent: &avsproto.EventTrigger_Config{
			ChainId: 84532,
			Queries: []*avsproto.EventTrigger_Query{{
				Addresses: []string{"0xbridge"},
				Topics:    []string{"0xdeadbeef"},
			}},
		},
		TimeoutAt: 1234,
	}
	extSub := &WakeSubscription{
		Kind:      WakeExternalSignal,
		External:  &ExternalSignalSpec{Channel: "telegram", Approvers: []string{"0xowner"}, Prompt: "approve?"},
		TimeoutAt: 5678,
	}

	require.NoError(t, persistWakeSubscription(db, "exec-A", chainSub))
	require.NoError(t, persistWakeSubscription(db, "exec-B", extSub))

	all, err := loadAllWakeSubscriptions(db)
	require.NoError(t, err)
	require.Len(t, all, 2)

	// Chain-event survived the protojson round-trip (chain_id + topics + addresses).
	a := all["exec-A"]
	require.NotNil(t, a)
	assert.Equal(t, WakeChainEvent, a.Kind)
	assert.Equal(t, int64(84532), a.ChainEvent.GetChainId())
	require.Len(t, a.ChainEvent.GetQueries(), 1)
	assert.Equal(t, []string{"0xdeadbeef"}, a.ChainEvent.GetQueries()[0].GetTopics())
	assert.Equal(t, []string{"0xbridge"}, a.ChainEvent.GetQueries()[0].GetAddresses())

	b := all["exec-B"]
	require.NotNil(t, b)
	assert.Equal(t, "telegram", b.External.Channel)
	assert.Equal(t, []string{"0xowner"}, b.External.Approvers)
	assert.Equal(t, int64(5678), b.TimeoutAt)

	// GC one; the scan reflects it.
	require.NoError(t, deleteWakeSubscription(db, "exec-A"))
	all, err = loadAllWakeSubscriptions(db)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Nil(t, all["exec-A"])
	assert.NotNil(t, all["exec-B"])
}
