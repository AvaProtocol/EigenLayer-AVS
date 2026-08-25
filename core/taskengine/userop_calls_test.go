package taskengine

import (
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInnerCallsFromExecuteCalldata_Execute(t *testing.T) {
	token := common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	data := []byte{0x09, 0x5e, 0xa7, 0xb3, 0x01, 0x02, 0x03}

	packed, err := aa.PackExecute(token, big.NewInt(7), data)
	require.NoError(t, err)

	calls, err := InnerCallsFromExecuteCalldata(packed)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, token.Hex(), calls[0].To)
	assert.Equal(t, "7", calls[0].Value)
	assert.Equal(t, "0x095ea7b3", calls[0].Selector)
	assert.Equal(t, "0x095ea7b3010203", calls[0].Data)
}

func TestInnerCallsFromExecuteCalldata_ExecuteBatch(t *testing.T) {
	token := common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	router := common.HexToAddress("0xbbbb000000000000000000000000000000000002")
	dataA := []byte{0x09, 0x5e, 0xa7, 0xb3}
	dataB := []byte{0x12, 0x34, 0x56, 0x78, 0x00}

	packed, err := aa.PackExecuteBatch([]common.Address{token, router}, [][]byte{dataA, dataB})
	require.NoError(t, err)

	calls, err := InnerCallsFromExecuteCalldata(packed)
	require.NoError(t, err)
	require.Len(t, calls, 2)
	assert.Equal(t, token.Hex(), calls[0].To)
	assert.Equal(t, "0", calls[0].Value)
	assert.Equal(t, "0x095ea7b3", calls[0].Selector)
	assert.Equal(t, router.Hex(), calls[1].To)
	assert.Equal(t, "0x12345678", calls[1].Selector)
}

func TestInnerCallsFromExecuteCalldata_Empty(t *testing.T) {
	calls, err := InnerCallsFromExecuteCalldata(nil)
	require.NoError(t, err)
	assert.Nil(t, calls)
}

func TestFailedReceiptWithInnerCalls_SingleCallSetsFailedCall(t *testing.T) {
	token := common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	packed, err := aa.PackExecute(token, big.NewInt(0), []byte{0xa9, 0x05, 0x9c, 0xbb})
	require.NoError(t, err)

	receipt := failedReceiptWithInnerCalls(packed)
	require.NotNil(t, receipt)
	m, ok := receipt.AsInterface().(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "failed", m["executionStatus"])
	calls, ok := m["calls"].([]interface{})
	require.True(t, ok)
	require.Len(t, calls, 1)
	failed, ok := m["failedCall"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, token.Hex(), failed["to"])
	assert.Equal(t, "0xa9059cbb", failed["selector"])
}

func TestFailedReceiptWithInnerCalls_BatchOmitsFailedCall(t *testing.T) {
	token := common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	router := common.HexToAddress("0xbbbb000000000000000000000000000000000002")
	packed, err := aa.PackExecuteBatch(
		[]common.Address{token, router},
		[][]byte{{0x09, 0x5e, 0xa7, 0xb3}, {0x12, 0x34, 0x56, 0x78}},
	)
	require.NoError(t, err)

	receipt := failedReceiptWithInnerCalls(packed)
	require.NotNil(t, receipt)
	m := receipt.AsInterface().(map[string]interface{})
	calls := m["calls"].([]interface{})
	require.Len(t, calls, 2)
	_, hasFailed := m["failedCall"]
	assert.False(t, hasFailed, "atomic batch does not invent which sub-call failed")
}
