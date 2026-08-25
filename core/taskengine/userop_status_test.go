package taskengine

import (
	"math/big"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeUserOpHash(t *testing.T) {
	want := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	got, err := normalizeUserOpHash("0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	require.NoError(t, err)
	assert.Equal(t, want, strings.ToLower(got))

	_, err = normalizeUserOpHash("0xabc")
	assert.ErrorIs(t, err, ErrUserOpHashInvalid)

	_, err = normalizeUserOpHash("not-hex")
	assert.ErrorIs(t, err, ErrUserOpHashInvalid)
}

func TestParseBundlerUserOp_Envelope(t *testing.T) {
	token := common.HexToAddress("0xaaaa000000000000000000000000000000000001")
	packed, err := aa.PackExecute(token, big.NewInt(0), []byte{0xa9, 0x05, 0x9c, 0xbb})
	require.NoError(t, err)

	sender := common.HexToAddress("0x2222222222222222222222222222222222222222")
	raw := map[string]interface{}{
		"userOperation": map[string]interface{}{
			"sender":   sender.Hex(),
			"callData": "0x" + common.Bytes2Hex(packed),
		},
		"transactionHash": "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	gotSender, callData, txHash, found := parseBundlerUserOp(raw)
	require.True(t, found)
	assert.Equal(t, sender, gotSender)
	assert.Equal(t, packed, callData)
	assert.Equal(t, "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", txHash)

	calls, err := InnerCallsFromExecuteCalldata(callData)
	require.NoError(t, err)
	require.Len(t, calls, 1)
	assert.Equal(t, token.Hex(), calls[0].To)
}

func TestParseBundlerUserOp_FlatAndNil(t *testing.T) {
	sender := common.HexToAddress("0x3333333333333333333333333333333333333333")
	gotSender, _, _, found := parseBundlerUserOp(map[string]interface{}{
		"sender":   sender.Hex(),
		"callData": "0x",
	})
	require.True(t, found)
	assert.Equal(t, sender, gotSender)

	_, _, _, found = parseBundlerUserOp(nil)
	assert.False(t, found)
	_, _, _, found = parseBundlerUserOp(map[string]interface{}{})
	assert.False(t, found)
}

func TestParseBundlerReceipt_SuccessAndNested(t *testing.T) {
	success, txHash, block := parseBundlerReceipt(map[string]interface{}{
		"success": true,
		"receipt": map[string]interface{}{
			"transactionHash": "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			"blockNumber":     "0x64",
		},
	})
	require.NotNil(t, success)
	assert.True(t, *success)
	assert.Equal(t, "0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", txHash)
	assert.Equal(t, "0x64", block)

	success, txHash, block = parseBundlerReceipt(nil)
	assert.Nil(t, success)
	assert.Empty(t, txHash)
	assert.Empty(t, block)

	failed := false
	success, _, _ = parseBundlerReceipt(map[string]interface{}{"success": failed})
	require.NotNil(t, success)
	assert.False(t, *success)
}
