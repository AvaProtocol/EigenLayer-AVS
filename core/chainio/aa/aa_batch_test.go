package aa

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// UnpackExecuteCalldata is the inverse of the pack helpers. These round-trips lock that contract:
// whatever we pack, we can decode back into the same (target, value, data) tuples.
func TestUnpackExecuteCalldata_Roundtrip(t *testing.T) {
	tokenA := common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	router := common.HexToAddress("0xbbbb000000000000000000000000000000000002")
	dataA := []byte{0x09, 0x5e, 0xa7, 0xb3, 0x01, 0x02, 0x03}
	dataB := []byte{0x12, 0x34, 0x56}

	t.Run("execute -> one entry", func(t *testing.T) {
		packed, err := PackExecuteMAv2(tokenA, big.NewInt(7), dataA)
		require.NoError(t, err)

		targets, values, datas, err := UnpackExecuteCalldata(packed)
		require.NoError(t, err)
		require.Len(t, targets, 1)
		assert.Equal(t, tokenA, targets[0])
		assert.Equal(t, int64(7), values[0].Int64())
		assert.True(t, bytes.Equal(dataA, datas[0]))
	})

	t.Run("executeUserOp-wrapped execute -> one entry", func(t *testing.T) {
		packed, err := PackExecuteMAv2(tokenA, big.NewInt(7), dataA)
		require.NoError(t, err)
		wrapped, err := WrapExecuteUserOp(packed)
		require.NoError(t, err)

		targets, values, datas, err := UnpackExecuteCalldata(wrapped)
		require.NoError(t, err)
		require.Len(t, targets, 1)
		assert.Equal(t, tokenA, targets[0])
		assert.Equal(t, int64(7), values[0].Int64())
		assert.True(t, bytes.Equal(dataA, datas[0]))
	})

	t.Run("executeBatch -> N entries with per-call values", func(t *testing.T) {
		packed, err := PackExecuteBatchMAv2([]Call{
			{Target: tokenA, Value: big.NewInt(0), Data: dataA},
			{Target: router, Value: big.NewInt(5), Data: dataB},
		})
		require.NoError(t, err)

		targets, values, datas, err := UnpackExecuteCalldata(packed)
		require.NoError(t, err)
		require.Len(t, targets, 2)
		assert.Equal(t, tokenA, targets[0])
		assert.Equal(t, router, targets[1])
		assert.Equal(t, int64(0), values[0].Int64())
		assert.Equal(t, int64(5), values[1].Int64())
		assert.True(t, bytes.Equal(dataA, datas[0]))
		assert.True(t, bytes.Equal(dataB, datas[1]))
	})

	t.Run("rejects the v0.6 executeBatch selector", func(t *testing.T) {
		// executeBatch(address[],bytes[]), which no MA v2 account implements.
		_, _, _, err := UnpackExecuteCalldata([]byte{0x18, 0xdf, 0xb3, 0xc7, 0x00})
		require.Error(t, err)
	})

	t.Run("rejects unknown selector", func(t *testing.T) {
		_, _, _, err := UnpackExecuteCalldata([]byte{0xde, 0xad, 0xbe, 0xef, 0x00})
		require.Error(t, err)
	})

	t.Run("rejects short calldata", func(t *testing.T) {
		_, _, _, err := UnpackExecuteCalldata([]byte{0x01, 0x02})
		require.Error(t, err)
	})
}
