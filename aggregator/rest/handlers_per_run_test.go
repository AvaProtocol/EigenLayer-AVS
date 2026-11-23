package rest

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

// perRun maps beside required. It never reports gas: its native cap is ETH
// sent, and the unsized markers say which amounts the owner must enter.
func TestSessionAuthorizationToAPIMapsPerRun(t *testing.T) {
	usdc := common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")
	usdt := common.HexToAddress("0x7169D38820dfd117C3FA1f22a697dBA58d90BA06")
	need := func(amount string) *taskengine.WorkflowNeed {
		return &taskengine.WorkflowNeed{
			Actions:        []model.AllowedAction{{Target: &usdc, Selectors: []string{"0xa9059cbb"}}},
			Caps:           []model.ERC20SpendCap{{Token: &usdc, Amount: amount}},
			NativeSpendCap: &model.NativeSpendCap{Amount: "1000"},
			NativeGasWei:   big.NewInt(400),
		}
	}
	out := sessionAuthorizationToAPI(&taskengine.SessionAuthorization{
		Status:              taskengine.AuthNoGrant,
		Required:            need("84"),
		PerRun:              need("7"),
		PerRunUnsizedTokens: []common.Address{usdt},
		PerRunNativeUnsized: true,
	})

	require.NotNil(t, out.Required)
	require.Equal(t, "84", (*out.Required.Erc20SpendCaps)[0].Amount)
	require.NotNil(t, out.Required.NativeGasWei, "required keeps its gas")

	require.NotNil(t, out.PerRun)
	require.Equal(t, "7", (*out.PerRun.Erc20SpendCaps)[0].Amount)
	require.Nil(t, out.PerRun.NativeGasWei, "one run's need reports no gas")
	require.NotNil(t, out.PerRunUnsizedTokens)
	require.Equal(t, usdt.Hex(), string((*out.PerRunUnsizedTokens)[0]))
	require.NotNil(t, out.PerRunNativeUnsized)
	require.True(t, *out.PerRunNativeUnsized)

	bare := sessionAuthorizationToAPI(&taskengine.SessionAuthorization{Status: taskengine.AuthCovered})
	require.Nil(t, bare.PerRun)
	require.Nil(t, bare.PerRunUnsizedTokens)
	require.Nil(t, bare.PerRunNativeUnsized)
}
