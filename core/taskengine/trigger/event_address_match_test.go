package trigger

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

const (
	eventMatchUSDC   = "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
	eventMatchSystem = "0xfffffffffffffffffffffffffffffffffffffffe"
	eventMatchZero   = "0x0000000000000000000000000000000000000000"
)

func newAddressMatchTrigger() *EventTrigger {
	return &EventTrigger{
		CommonTrigger: &CommonTrigger{logger: &MockLogger{}},
		registry:      NewTaskRegistry(),
		eventCounts:   map[string]map[uint64]uint32{},
		updateSubsCh:  make(chan struct{}, 1),
	}
}

func eventTask(taskID string, queries ...*avsproto.EventTrigger_Query) *avsproto.SyncMessagesResp_TaskMetadata {
	return &avsproto.SyncMessagesResp_TaskMetadata{
		TaskId: taskID,
		Trigger: &avsproto.TaskTrigger{
			TriggerType: &avsproto.TaskTrigger_Event{
				Event: &avsproto.EventTrigger{
					Config: &avsproto.EventTrigger_Config{Queries: queries},
				},
			},
		},
	}
}

func TestEventTrigger_LogMatchesNeedsAContract(t *testing.T) {
	trigger := newAddressMatchTrigger()
	usdcLog := types.Log{Address: common.HexToAddress(eventMatchUSDC)}
	systemLog := types.Log{Address: common.HexToAddress(eventMatchSystem)}

	assert.False(t, trigger.logMatchesEventQuery(usdcLog, &avsproto.EventTrigger_Query{}, nil, 0))
	assert.False(t, trigger.logMatchesEventQuery(systemLog, &avsproto.EventTrigger_Query{
		Addresses: []string{eventMatchSystem, eventMatchUSDC},
	}, nil, 0))
	assert.True(t, trigger.logMatchesEventQuery(usdcLog, &avsproto.EventTrigger_Query{
		Addresses: []string{eventMatchSystem, eventMatchUSDC},
	}, nil, 0))
	assert.False(t, trigger.logMatchesEventQuery(usdcLog, &avsproto.EventTrigger_Query{
		Addresses: []string{eventMatchSystem},
	}, nil, 0))
}

func TestEventTrigger_AddCheckRequiresAContract(t *testing.T) {
	cases := [][]string{
		{},
		{eventMatchSystem},
		{eventMatchZero},
	}
	for _, addresses := range cases {
		trigger := newAddressMatchTrigger()
		err := trigger.AddCheck(eventTask("task-empty", &avsproto.EventTrigger_Query{Addresses: addresses}))
		require.EqualError(t, err, "queries[].addresses must be a non-empty array")
		assert.Equal(t, 0, trigger.registry.GetEventTaskCount())
	}

	trigger := newAddressMatchTrigger()
	err := trigger.AddCheck(eventTask("task-mixed",
		&avsproto.EventTrigger_Query{Addresses: []string{eventMatchSystem}},
		&avsproto.EventTrigger_Query{Addresses: []string{eventMatchZero, eventMatchUSDC}},
	))
	require.NoError(t, err)
	assert.Equal(t, 1, trigger.registry.GetEventTaskCount())

	queries := trigger.buildFilterQueries()
	require.Len(t, queries, 1)
	assert.Equal(t, []common.Address{common.HexToAddress(eventMatchUSDC)}, queries[0].Query.Addresses)
}

func TestEventTrigger_ConvertDropsSystemAddress(t *testing.T) {
	trigger := newAddressMatchTrigger()
	got := trigger.convertToFilterQuery(&avsproto.EventTrigger_Query{
		Addresses: []string{eventMatchZero, eventMatchSystem, eventMatchUSDC},
	})
	assert.Equal(t, []common.Address{common.HexToAddress(eventMatchUSDC)}, got.Addresses)

	empty := trigger.convertToFilterQuery(&avsproto.EventTrigger_Query{})
	assert.Empty(t, empty.Addresses)
}
