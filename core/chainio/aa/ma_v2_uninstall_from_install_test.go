package aa

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

// The teardown these tests pin was established on Sepolia while spiking #717.
// Two payload shapes were tried against the same batch. Both MINED with
// success=true. Only one actually cleared anything:
//
//	601,275 gas  entity kept its signer AND its full 100-token spend cap
//	636,341 gas  signer cleared to 0x0, cap cleared to 0
//
// Nothing on chain distinguishes them — the account catches onUninstall
// reverts and strands the module state. So these assertions are the guard: if
// the ordering or the payloads drift, no test that watches a receipt will
// notice, and a wallet keeps a live spend cap the product believes is revoked.

const (
	testEntity  = uint32(1)
	spikeCapWei = 100
)

func testGrantInstall(t *testing.T, entity uint32) (call []byte, token common.Address, allowlistData, timeRangeData []byte) {
	t.Helper()
	token = common.HexToAddress("0xaA4D01B75fdEB5fbbD98276EC7755eF71801c2E7")
	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")

	inputs := []AllowlistInput{{
		Target:               token,
		HasSelectorAllowlist: true,
		Selectors:            [][4]byte{{0xa9, 0x05, 0x9c, 0xbb}},
		HasERC20SpendLimit:   true,
		ERC20SpendLimit:      new(big.Int).Mul(big.NewInt(spikeCapWei), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)),
	}}

	allowlistHook, err := AllowlistValidationHook(entity, inputs)
	require.NoError(t, err)
	timeRangeHook, err := TimeRangeValidationHook(entity, 1785541743, 0)
	require.NoError(t, err)

	// Install order, matching SessionPermissions.HooksFor exactly.
	call, err = PackSessionSignerInstall(SessionGrant{
		EntityID: entity,
		Signer:   signer,
		Global:   true,
		Hooks:    [][]byte{allowlistHook, AllowlistExecHook(entity), timeRangeHook},
	})
	require.NoError(t, err)

	allowlistData, err = PackAllowlistInstallData(entity, inputs)
	require.NoError(t, err)
	timeRangeData, err = PackTimeRangeInstallData(entity, 1785541743, 0)
	require.NoError(t, err)
	return call, token, allowlistData, timeRangeData
}

func TestDecodeInstallValidationHooksRecoversInstallOrder(t *testing.T) {
	call, _, allowlistData, timeRangeData := testGrantInstall(t, testEntity)

	hooks, err := DecodeInstallValidationHooks(call)
	require.NoError(t, err)
	require.Len(t, hooks, 3, "a policied grant installs allowlist-validation, allowlist-exec, time-range")

	// Each entry is config(25) ++ that module's install data.
	require.Equal(t, allowlistData, hooks[0][hookConfigLen:], "hook 0 carries the allowlist tuple")
	require.Len(t, hooks[1], hookConfigLen, "the exec hook carries no install data — its state belongs to hook 0")
	require.Equal(t, timeRangeData, hooks[2][hookConfigLen:], "hook 2 carries the time range")
}

// The ordering finding, stated as an assertion. Install order is
// [allowlist-validation, allowlist-exec, time-range]; the account stores hooks
// prepend-on-add, so teardown must be the reverse.
func TestUninstallReversesIntoStoredOrder(t *testing.T) {
	call, _, allowlistData, timeRangeData := testGrantInstall(t, testEntity)

	uninstall, err := SessionSignerUninstallFromInstall(testEntity, call)
	require.NoError(t, err)

	hookData := decodeUninstallHookData(t, uninstall)
	require.Len(t, hookData, 3, "one entry per installed hook, or the account reverts ArrayLengthMismatch")

	require.Equal(t, timeRangeData, hookData[0],
		"stored order puts the LAST-installed hook first — time range")
	require.Empty(t, hookData[1],
		"the allowlist's exec-hook entry installed no state, so its teardown slot is empty")
	require.Equal(t, allowlistData, hookData[2],
		"the allowlist's teardown is the same (entityId, inputs) tuple it was installed with")
}

// Rebuilding the payload from current code instead of the stored call is the
// mistake this whole file exists to prevent: it compiles, it mines, and it
// clears nothing. Pinning the derivation to the stored bytes means a grant
// whose shape predates a code change still tears down correctly.
func TestTeardownFollowsTheStoredCallNotCurrentCode(t *testing.T) {
	call, _, allowlistData, _ := testGrantInstall(t, testEntity)

	// A grant installed with a DIFFERENT cap — as an older policy would have.
	otherInputs := []AllowlistInput{{
		Target:               common.HexToAddress("0xaA4D01B75fdEB5fbbD98276EC7755eF71801c2E7"),
		HasSelectorAllowlist: true,
		Selectors:            [][4]byte{{0xa9, 0x05, 0x9c, 0xbb}},
		HasERC20SpendLimit:   true,
		ERC20SpendLimit:      big.NewInt(7), // nothing today would produce this
	}}
	otherHook, err := AllowlistValidationHook(testEntity, otherInputs)
	require.NoError(t, err)
	timeRangeHook, err := TimeRangeValidationHook(testEntity, 1785541743, 0)
	require.NoError(t, err)
	otherCall, err := PackSessionSignerInstall(SessionGrant{
		EntityID: testEntity,
		Signer:   common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB"),
		Global:   true,
		Hooks:    [][]byte{otherHook, AllowlistExecHook(testEntity), timeRangeHook},
	})
	require.NoError(t, err)

	otherTeardown, err := SessionSignerUninstallFromInstall(testEntity, otherCall)
	require.NoError(t, err)
	otherData := decodeUninstallHookData(t, otherTeardown)

	otherAllowlistData, err := PackAllowlistInstallData(testEntity, otherInputs)
	require.NoError(t, err)
	require.Equal(t, otherAllowlistData, otherData[2],
		"teardown must reproduce the cap the entity was installed with, not the current one")
	require.NotEqual(t, allowlistData, otherData[2],
		"precondition: the two grants really do differ")

	// And the round trip is stable for the original.
	teardown, err := SessionSignerUninstallFromInstall(testEntity, call)
	require.NoError(t, err)
	require.Equal(t, allowlistData, decodeUninstallHookData(t, teardown)[2])
}

func TestUninstallFromInstallRejectsUnusableInput(t *testing.T) {
	call, _, _, _ := testGrantInstall(t, testEntity)

	_, err := SessionSignerUninstallFromInstall(testEntity, nil)
	require.Error(t, err, "no stored call means no faithful teardown — refuse rather than guess")

	_, err = SessionSignerUninstallFromInstall(testEntity, []byte{0xde, 0xad, 0xbe, 0xef})
	require.Error(t, err)

	// Entity 0 is the owner's fallback signer and must never be torn down.
	_, err = SessionSignerUninstallFromInstall(0, call)
	require.Error(t, err)
}

// decodeUninstallHookData pulls hookUninstallData back out of packed
// uninstallValidation calldata.
func decodeUninstallHookData(t *testing.T, uninstallCall []byte) [][]byte {
	t.Helper()
	require.NoError(t, ensureUninstallABI())
	method, ok := uninstallABI.Methods["uninstallValidation"]
	require.True(t, ok)
	require.Equal(t, method.ID, uninstallCall[:4])

	args, err := method.Inputs.Unpack(uninstallCall[4:])
	require.NoError(t, err)
	require.Len(t, args, 3)
	hookData, ok := args[2].([][]byte)
	require.True(t, ok)
	return hookData
}
