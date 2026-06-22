package taskengine

import (
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseUint256(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    *big.Int
		wantErr bool
	}{
		{"decimal", "1000000", big.NewInt(1000000), false},
		{"hex lower", "0x38d7ea4c68000", big.NewInt(1000000000000000), false},
		{"hex upper prefix", "0X10", big.NewInt(16), false},
		{"max uint256", "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)), false},
		{"whitespace", "  42  ", big.NewInt(42), false},
		{"empty", "", nil, true},
		{"garbage", "0xnothex", nil, true},
		{"not decimal", "12ab", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseUint256(tc.in)
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 0, tc.want.Cmp(got), "want %s got %s", tc.want, got)
		})
	}
}

func TestApplyUserERC20Override_BalanceAndAllowance(t *testing.T) {
	logger := testutil.GetLogger()
	state := NewSimulationStateMap(logger)

	token := "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
	owner := "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e"
	spender := "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E"

	balSlot := uint64(0)
	allowSlot := uint64(3)
	err := state.ApplyUserERC20Override(
		token, owner, spender,
		"1000000", // balance (decimal)
		"0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", // allowance (max uint256)
		&balSlot, &allowSlot,
	)
	require.NoError(t, err)

	objects := state.BuildStateObjects(owner, "0x100")
	tokenObj, ok := objects[common.HexToAddress(token).Hex()]
	// BuildStateObjects lowercases keys, so look it up lowercased.
	if !ok {
		tokenObj, ok = objects["0x1c7d4b196cb0c7b01d743fbc6116a902379c7238"]
	}
	require.True(t, ok, "token should appear in state_objects: %v", objects)
	storage, ok := tokenObj.(map[string]interface{})["storage"].(map[string]string)
	require.True(t, ok)

	// Balance slot value: keccak256(abi.encode(owner, 0)) -> 0x...0f4240 (1,000,000)
	wantBalSlot := erc20BalanceSlot(common.HexToAddress(owner), 0).Hex()
	assert.Equal(t, "0x00000000000000000000000000000000000000000000000000000000000f4240", storage[wantBalSlot])

	// Allowance slot value: max uint256
	wantAllowSlot := erc20AllowanceSlot(common.HexToAddress(owner), common.HexToAddress(spender), 3).Hex()
	assert.Equal(t, "0x"+"ff"+"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", storage[wantAllowSlot])
}

func TestApplyUserERC20Override_DefaultSlots(t *testing.T) {
	state := NewSimulationStateMap(testutil.GetLogger())
	token := "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
	owner := "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e"
	spender := "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E"

	// No slots supplied — should fall back to defaults (0 balance, 3 allowance).
	require.NoError(t, state.ApplyUserERC20Override(token, owner, spender, "5", "10", nil, nil))

	objects := state.BuildStateObjects(owner, "0x0")
	tokenObj := objects["0x1c7d4b196cb0c7b01d743fbc6116a902379c7238"].(map[string]interface{})
	storage := tokenObj["storage"].(map[string]string)

	wantBal := erc20BalanceSlot(common.HexToAddress(owner), defaultERC20BalanceSlot).Hex()
	wantAllow := erc20AllowanceSlot(common.HexToAddress(owner), common.HexToAddress(spender), defaultERC20AllowanceSlot).Hex()
	_, hasBal := storage[wantBal]
	_, hasAllow := storage[wantAllow]
	assert.True(t, hasBal, "balance override at default slot 0")
	assert.True(t, hasAllow, "allowance override at default slot 3")
}

func TestApplyUserERC20Override_Validation(t *testing.T) {
	state := NewSimulationStateMap(testutil.GetLogger())
	owner := "0x71c8f4D7D5291EdCb3A081802e7efB2788Bd232e"
	token := "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"

	t.Run("bad token address", func(t *testing.T) {
		err := state.ApplyUserERC20Override("not-an-address", owner, "", "1", "", nil, nil)
		assert.Error(t, err)
	})
	t.Run("bad owner address", func(t *testing.T) {
		err := state.ApplyUserERC20Override(token, "0x123", "", "1", "", nil, nil)
		assert.Error(t, err)
	})
	t.Run("neither balance nor allowance", func(t *testing.T) {
		err := state.ApplyUserERC20Override(token, owner, "", "", "", nil, nil)
		assert.Error(t, err)
	})
	t.Run("allowance without spender", func(t *testing.T) {
		err := state.ApplyUserERC20Override(token, owner, "", "", "10", nil, nil)
		assert.Error(t, err)
	})
	t.Run("balance only is fine", func(t *testing.T) {
		err := state.ApplyUserERC20Override(token, owner, "", "1", "", nil, nil)
		assert.NoError(t, err)
	})
}
