package taskengine

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func eventTriggerWithAddresses(queries ...[]string) *avsproto.TaskTrigger {
	protoQueries := make([]*avsproto.EventTrigger_Query, 0, len(queries))
	for _, addresses := range queries {
		protoQueries = append(protoQueries, &avsproto.EventTrigger_Query{Addresses: addresses})
	}
	return &avsproto.TaskTrigger{
		TriggerType: &avsproto.TaskTrigger_Event{
			Event: &avsproto.EventTrigger{
				Config: &avsproto.EventTrigger_Config{Queries: protoQueries},
			},
		},
	}
}

func TestUsableEventQueryAddresses(t *testing.T) {
	const (
		usdc   = "0x1c7d4b196cb0c7b01d743fbc6116a902379c7238"
		system = "0xFFffFFffFFfffFFfFFfFFFFFffFFFffffFfFFFfE"
		zero   = "0x0000000000000000000000000000000000000000"
	)

	got := UsableEventQueryAddresses([]string{zero, system, "not-an-address", usdc, usdc})
	assert.Equal(t, []string{common.HexToAddress(usdc).Hex()}, got)
	assert.Empty(t, UsableEventQueryAddresses(nil))
	assert.True(t, IsEthTransferLogAddress(common.HexToAddress(system)))
	assert.False(t, IsEthTransferLogAddress(common.HexToAddress(usdc)))
}

func TestValidateEventTriggerAddresses(t *testing.T) {
	const (
		usdc   = "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
		system = "0xfffffffffffffffffffffffffffffffffffffffe"
		zero   = "0x0000000000000000000000000000000000000000"
	)

	require.NoError(t, ValidateEventTriggerAddresses(nil))
	require.NoError(t, ValidateEventTriggerAddresses(&avsproto.TaskTrigger{
		TriggerType: &avsproto.TaskTrigger_Manual{},
	}))
	require.NoError(t, ValidateEventTriggerAddresses(eventTriggerWithAddresses([]string{usdc, system})))

	err := ValidateEventTriggerAddresses(eventTriggerWithAddresses([]string{}))
	require.EqualError(t, err, "queries[].addresses must be a non-empty array")

	err = ValidateEventTriggerAddresses(eventTriggerWithAddresses([]string{zero}))
	require.EqualError(t, err, "queries[].addresses must be a non-empty array")

	err = ValidateEventTriggerAddresses(eventTriggerWithAddresses([]string{usdc}, []string{system}))
	require.EqualError(t, err, "query[1].addresses: the ETH transfer system address cannot be watched")
}
