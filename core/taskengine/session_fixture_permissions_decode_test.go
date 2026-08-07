package taskengine

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
)

// The permission comparison decided offline, against a real answer from the
// chain. sepoliaEntity1ValidationData is the verbatim getValidationData return
// for entity 1 on 0x209eb31c199bEB4c386eF83CF442DE1a00667a1F (Sepolia) — the
// shared fixture runner whose stale grant produced the AA23 in
// TestUserOpETHWithdrawal_Sepolia. Using the real bytes rather than a
// hand-built fixture is the point: the decoder has to survive what the account
// actually returns.
const sepoliaEntity1ValidationData = "" +
	"0000000000000000000000000000000000000000000000000000000000000020" +
	"0000000000000000000000000000000000000000000000000000000000000005" +
	"0000000000000000000000000000000000000000000000000000000000000080" +
	"00000000000000000000000000000000000000000000000000000000000000e0" +
	"0000000000000000000000000000000000000000000000000000000000000120" +
	"0000000000000000000000000000000000000000000000000000000000000002" +
	"00000000003e826473a313e600b5b9b791f5a59a000000010100000000000000" +
	"00000000000082b8e2012be914dfa4f62a0573ea000000010100000000000000" +
	"0000000000000000000000000000000000000000000000000000000000000001" +
	"00000000003e826473a313e600b5b9b791f5a59a000000010400000000000000" +
	"0000000000000000000000000000000000000000000000000000000000000000"

var fixtureController = common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")

func TestDecodeValidationDataFromSepolia(t *testing.T) {
	state, err := decodeValidationData(common.FromHex(sepoliaEntity1ValidationData))
	require.NoError(t, err)

	require.Equal(t, []hookRef{
		{Module: aa.AllowlistModuleAddress(), Entity: 1, Flags: aa.HookFlagValidation},
		{Module: aa.TimeRangeModuleAddress(), Entity: 1, Flags: aa.HookFlagValidation},
	}, state.ValidationHooks, "entity 1 carries the allowlist and time-range validation hooks")

	require.Equal(t, []hookRef{
		{Module: aa.AllowlistModuleAddress(), Entity: 1, Flags: aa.HookFlagExecHasPre},
	}, state.ExecutionHooks, "and an allowlist EXECUTION hook — the half that forces executeUserOp wrapping")

	require.Zero(t, state.SelectorCount, "the validation is global")
	require.Equal(t, aa.ValidationFlagUserOp|aa.ValidationFlagGlobal, state.Flags,
		"the validation itself is UserOp + Global; flags are not inferable from the selector list")
}

// The regression in one assertion: the old fixture check compared the signer
// and nothing else, so it adopted this entity for a test that needed a bare
// grant. The signer does match. Everything that decides whether the account
// accepts the operation does not.
// fixtureEntity is the entity the captured validation data above describes.
const fixtureEntity = uint32(1)

func TestFixtureGrantRejectsTheStaleHookedEntity(t *testing.T) {
	state, err := decodeValidationData(common.FromHex(sepoliaEntity1ValidationData))
	require.NoError(t, err)
	state.Installed = true
	state.Signer = fixtureController

	require.Equal(t, fixtureController, state.Signer,
		"precondition: the signer matches, which is all the old check looked at")

	want := timeBoxedGlobalGrant(fixtureController, fixtureEntity)
	require.False(t, want.matches(state), "a fixture grant must not adopt an entity carrying a different hook set")

	diff := want.diff(state)
	require.Contains(t, diff, "validation hooks")
	require.Contains(t, diff, "execution hooks")
	require.Contains(t, diff, aa.AllowlistModuleAddress().Hex(),
		"the diff names the module that is actually installed, so the reader does not need a block explorer")
}

// The production-shaped fixture describes this entity's MODULES exactly — and
// still must not adopt it.
//
// A HookConfig names a module and an entity and says nothing about the
// configuration that module holds. The allowlist on entity 1 could permit
// approve() on USDC only while the fixture needs USDC and WETH, and nothing
// readable from the account tells them apart. Adopting on a shape match would
// reintroduce the stale-allowlist AA23 that #714 fixed by always installing
// fresh.
func TestHookedGrantFixtureDoesNotAdoptOnAShapeMatch(t *testing.T) {
	state, err := decodeValidationData(common.FromHex(sepoliaEntity1ValidationData))
	require.NoError(t, err)
	state.Installed = true
	state.Signer = fixtureController

	want := hookedGlobalGrant(fixtureController, 1)
	require.True(t, want.matches(state),
		"precondition: the modules do match, which is exactly why matching alone is not enough: %s", want.diff(state))
	require.False(t, want.Reusable,
		"a fixture whose hooks carry module state must install fresh, never adopt")
}

// The bare fixture is the one shape that may be adopted: no hooks means no
// module state hiding behind the comparison.
func TestBareGrantFixtureIsTheOnlyReusableShape(t *testing.T) {
	require.True(t, timeBoxedGlobalGrant(fixtureController, fixtureEntity).Reusable)
	require.False(t, hookedGlobalGrant(fixtureController, 1).Reusable)
}

func TestFixturePermissionDiffs(t *testing.T) {
	hooked := func() validationState {
		state, err := decodeValidationData(common.FromHex(sepoliaEntity1ValidationData))
		require.NoError(t, err)
		state.Installed = true
		state.Signer = fixtureController
		return state
	}

	t.Run("a free entity is reported as free, not as a mismatch", func(t *testing.T) {
		want := timeBoxedGlobalGrant(fixtureController, fixtureEntity)
		require.Equal(t, "entity is free (no signer installed)", want.diff(validationState{Installed: false}))
	})

	t.Run("a foreign signer is named on both sides", func(t *testing.T) {
		stranger := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
		state := hooked()
		state.Signer = stranger
		diff := hookedGlobalGrant(fixtureController, 1).diff(state)
		require.Contains(t, diff, stranger.Hex())
		require.Contains(t, diff, fixtureController.Hex())
	})

	t.Run("hook sets compare as sets, not as ordered lists", func(t *testing.T) {
		state := hooked()
		state.ValidationHooks = []hookRef{state.ValidationHooks[1], state.ValidationHooks[0]}
		require.True(t, hookedGlobalGrant(fixtureController, 1).matches(state),
			"the account stores hooks prepend-on-add, so stored order is not install order")
	})

	t.Run("hooks at another entity are a different grant", func(t *testing.T) {
		state := hooked()
		require.False(t, hookedGlobalGrant(fixtureController, 2).matches(state),
			"same modules at a different entity is not the same authorization")
	})

	t.Run("selector scoping is compared for a global fixture", func(t *testing.T) {
		state := hooked()
		state.SelectorCount = 3
		require.Contains(t, hookedGlobalGrant(fixtureController, 1).diff(state), "want global")
	})

	// An empty selector list does not imply global authority, and signature
	// authority is invisible in it — both live in the validation flags.
	t.Run("validation flags are compared, not inferred from selectors", func(t *testing.T) {
		state := hooked()
		state.Flags = aa.ValidationFlagUserOp // no Global bit, still no selectors
		require.Contains(t, hookedGlobalGrant(fixtureController, 1).diff(state), "validation flags are 0x01")

		state = hooked()
		state.Flags = aa.ValidationFlagUserOp | aa.ValidationFlagGlobal | aa.ValidationFlagSignature
		require.Contains(t, hookedGlobalGrant(fixtureController, 1).diff(state), "validation flags are 0x07",
			"signature authority this fixture never asked for must not pass unnoticed")
	})

	// The same module registered for a different callback is a different
	// authorization: a post-only execution hook does not enforce what a
	// pre-execution hook enforces.
	t.Run("hook callback flags are compared", func(t *testing.T) {
		state := hooked()
		state.ExecutionHooks = []hookRef{{
			Module: aa.AllowlistModuleAddress(), Entity: 1, Flags: aa.HookFlagExecHasPost,
		}}
		require.Contains(t, hookedGlobalGrant(fixtureController, 1).diff(state), "execution hooks")
	})
}
