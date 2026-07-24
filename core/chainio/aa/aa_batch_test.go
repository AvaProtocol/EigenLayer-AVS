package aa

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// UnpackExecuteCalldata is the inverse of the three pack helpers and is what lets the paymaster
// reimbursement wrapper append its entries onto an already-batched call. These round-trips lock
// that contract: whatever we pack, we can decode back into the same (target, value, data) tuples.
func TestUnpackExecuteCalldata_Roundtrip(t *testing.T) {
	tokenA := common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	router := common.HexToAddress("0xbbbb000000000000000000000000000000000002")
	dataA := []byte{0x09, 0x5e, 0xa7, 0xb3, 0x01, 0x02, 0x03}
	dataB := []byte{0x12, 0x34, 0x56}

	t.Run("execute -> one entry", func(t *testing.T) {
		packed, err := PackExecute(tokenA, big.NewInt(7), dataA)
		require.NoError(t, err)

		targets, values, datas, err := UnpackExecuteCalldata(packed)
		require.NoError(t, err)
		require.Len(t, targets, 1)
		assert.Equal(t, tokenA, targets[0])
		assert.Equal(t, int64(7), values[0].Int64())
		assert.True(t, bytes.Equal(dataA, datas[0]))
	})

	t.Run("executeBatch -> N entries, values default to 0", func(t *testing.T) {
		packed, err := PackExecuteBatch([]common.Address{tokenA, router}, [][]byte{dataA, dataB})
		require.NoError(t, err)

		targets, values, datas, err := UnpackExecuteCalldata(packed)
		require.NoError(t, err)
		require.Len(t, targets, 2)
		assert.Equal(t, tokenA, targets[0])
		assert.Equal(t, router, targets[1])
		assert.Equal(t, int64(0), values[0].Int64())
		assert.Equal(t, int64(0), values[1].Int64())
		assert.True(t, bytes.Equal(dataA, datas[0]))
		assert.True(t, bytes.Equal(dataB, datas[1]))
	})

	t.Run("executeBatchWithValues -> N entries with explicit values", func(t *testing.T) {
		packed, err := PackExecuteBatchWithValues(
			[]common.Address{tokenA, router},
			[]*big.Int{big.NewInt(0), big.NewInt(5)},
			[][]byte{dataA, dataB},
		)
		require.NoError(t, err)

		targets, values, datas, err := UnpackExecuteCalldata(packed)
		require.NoError(t, err)
		require.Len(t, targets, 2)
		assert.Equal(t, int64(0), values[0].Int64())
		assert.Equal(t, int64(5), values[1].Int64())
		assert.True(t, bytes.Equal(dataA, datas[0]))
		assert.True(t, bytes.Equal(dataB, datas[1]))
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

// TestUnpackExecuteCalldata_ReimbursementComposition exercises exactly the transformation the
// reimbursement wrapper performs on an atomic approve+swap batch: decode the app calls, append a
// reimbursement (and fee) entry, and re-pack as executeBatchWithValues. The final calldata must
// decode back to app-calls-then-reimbursement-then-fee with the values summed the way the wallet
// will spend them. This is the crux that let G4 compose with paymaster reimbursement.
func TestUnpackExecuteCalldata_ReimbursementComposition(t *testing.T) {
	token := common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	router := common.HexToAddress("0xbbbb000000000000000000000000000000000002")
	paymasterOwner := common.HexToAddress("0xcccc000000000000000000000000000000000003")
	approveData := []byte{0x09, 0x5e, 0xa7, 0xb3}
	swapData := []byte{0x41, 0x4b, 0xf3, 0x89}
	reimbursement := big.NewInt(12345)
	fee := big.NewInt(678)

	// App batch: approve@token (value 0) + swap@router (value 0) — the token-in approve+swap case.
	appBatch, err := PackExecuteBatch([]common.Address{token, router}, [][]byte{approveData, swapData})
	require.NoError(t, err)

	// Decode + append reimbursement + fee (mirrors wrapWithReimbursement).
	targets, values, datas, err := UnpackExecuteCalldata(appBatch)
	require.NoError(t, err)
	targets = append(targets, paymasterOwner, paymasterOwner)
	values = append(values, reimbursement, fee)
	datas = append(datas, []byte{}, []byte{})

	wrapped, err := PackExecuteBatchWithValues(targets, values, datas)
	require.NoError(t, err)

	gotTargets, gotValues, gotDatas, err := UnpackExecuteCalldata(wrapped)
	require.NoError(t, err)
	require.Len(t, gotTargets, 4)
	assert.Equal(t, []common.Address{token, router, paymasterOwner, paymasterOwner}, gotTargets)
	assert.Equal(t, int64(0), gotValues[0].Int64())
	assert.Equal(t, int64(0), gotValues[1].Int64())
	assert.Equal(t, reimbursement.Int64(), gotValues[2].Int64())
	assert.Equal(t, fee.Int64(), gotValues[3].Int64())
	assert.True(t, bytes.Equal(approveData, gotDatas[0]))
	assert.True(t, bytes.Equal(swapData, gotDatas[1]))
	assert.Empty(t, gotDatas[2])
	assert.Empty(t, gotDatas[3])
}
