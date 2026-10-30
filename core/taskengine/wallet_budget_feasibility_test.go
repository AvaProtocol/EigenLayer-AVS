package taskengine

import (
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

// Test-only prototype of AllowlistModule.onInstall's ABI. The production
// parser AND encoder reject zero today; accepting zero is not a default change
// on this spike branch. Keep a control against the production nonzero encoding.
func budgetPrototypeAllowlistHook(t *testing.T, entity uint32, inputs []aa.AllowlistInput) []byte {
	t.Helper()
	u32, err := abi.NewType("uint32", "", nil)
	require.NoError(t, err)
	rows, err := abi.NewType("tuple[]", "", []abi.ArgumentMarshaling{
		{Name: "target", Type: "address"}, {Name: "hasSelectorAllowlist", Type: "bool"},
		{Name: "hasERC20SpendLimit", Type: "bool"}, {Name: "erc20SpendLimit", Type: "uint256"}, {Name: "selectors", Type: "bytes4[]"},
	})
	require.NoError(t, err)
	type row struct {
		Target               common.Address
		HasSelectorAllowlist bool
		HasERC20SpendLimit   bool
		Erc20SpendLimit      *big.Int
		Selectors            [][4]byte
	}
	values := make([]row, len(inputs))
	for i, in := range inputs {
		limit := in.ERC20SpendLimit
		if limit == nil {
			limit = big.NewInt(0)
		}
		values[i] = row{in.Target, in.HasSelectorAllowlist, in.HasERC20SpendLimit, limit, in.Selectors}
	}
	data, err := (abi.Arguments{{Type: u32}, {Type: rows}}).Pack(entity, values)
	require.NoError(t, err)
	config := aa.PackHookConfig(aa.AllowlistModuleAddress(), entity, aa.HookFlagValidation)
	return append(config[:], data...)
}

func TestWalletBudgetZeroCapPrototypePreservesProductionGuards(t *testing.T) {
	input := aa.AllowlistInput{Target: common.HexToAddress("0x1111111111111111111111111111111111111111"), HasSelectorAllowlist: true,
		HasERC20SpendLimit: true, ERC20SpendLimit: big.NewInt(1), Selectors: [][4]byte{{0x09, 0x5e, 0xa7, 0xb3}}}
	production, err := aa.AllowlistValidationHook(7, []aa.AllowlistInput{input})
	require.NoError(t, err)
	require.Equal(t, production, budgetPrototypeAllowlistHook(t, 7, []aa.AllowlistInput{input}))
	input.ERC20SpendLimit = big.NewInt(0)
	_, err = aa.AllowlistValidationHook(7, []aa.AllowlistInput{input})
	require.Error(t, err)
	_, err = parseCapAmount("0")
	require.Error(t, err)
	prototype := budgetPrototypeAllowlistHook(t, 7, []aa.AllowlistInput{input})
	// The amount occupies the penultimate word of the tuple header (the last
	// is its selector-array offset). Compare the full encoding with cap 1.
	require.Equal(t, len(production), len(prototype))
	diffs := 0
	for i := range prototype {
		if prototype[i] != production[i] {
			diffs++
			require.Equal(t, byte(1), production[i])
			require.Zero(t, prototype[i])
		}
	}
	require.Equal(t, 1, diffs, "only the uint256 cap changes; the metering flag stays enabled")
}
