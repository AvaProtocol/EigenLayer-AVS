package aa

import (
	"context"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"
)

func TestReadERC20SpendLimitUsesAllowlistAndRunner(t *testing.T) {
	token := common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")
	runner := common.HexToAddress("0x00000000000000000000000000000000000000a1")
	caller := &recordingCaller{result: common.LeftPadBytes(big.NewInt(60).Bytes(), 32)}

	got, err := ReadERC20SpendLimit(context.Background(), caller, 7, token, runner)
	require.NoError(t, err)
	require.Equal(t, "60", got.String())
	require.Equal(t, AllowlistModuleAddress(), caller.to)
	wantSel := crypto.Keccak256([]byte("erc20SpendLimits(uint32,address,address)"))[:4]
	require.Equal(t, wantSel, caller.data[:4])
	require.Equal(t, common.LeftPadBytes(big.NewInt(7).Bytes(), 32), caller.data[4:36])
	require.Equal(t, common.LeftPadBytes(token.Bytes(), 32), caller.data[36:68])
	require.Equal(t, common.LeftPadBytes(runner.Bytes(), 32), caller.data[68:100])
}

func TestReadNativeSpendLimitUsesNativeModuleUint256(t *testing.T) {
	runner := common.HexToAddress("0x00000000000000000000000000000000000000a1")
	caller := &recordingCaller{result: common.LeftPadBytes(big.NewInt(8).Bytes(), 32)}

	got, err := ReadNativeSpendLimit(context.Background(), caller, 7, runner)
	require.NoError(t, err)
	require.Equal(t, "8", got.String())
	require.Equal(t, NativeTokenLimitModuleAddress(), caller.to)
	wantSel := crypto.Keccak256([]byte("limits(uint256,address)"))[:4]
	require.Equal(t, wantSel, caller.data[:4])
	require.Equal(t, common.LeftPadBytes(big.NewInt(7).Bytes(), 32), caller.data[4:36])
	require.Equal(t, common.LeftPadBytes(runner.Bytes(), 32), caller.data[36:68])
	require.NotEqual(t, AllowlistModuleAddress(), caller.to)
}

func TestReadSpendLimitRejectsShortReturn(t *testing.T) {
	caller := &recordingCaller{result: []byte{0}}
	token := common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")
	runner := common.HexToAddress("0x00000000000000000000000000000000000000a1")
	_, err := ReadERC20SpendLimit(context.Background(), caller, 1, token, runner)
	require.Error(t, err)
	_, err = ReadNativeSpendLimit(context.Background(), caller, 1, runner)
	require.Error(t, err)
}
