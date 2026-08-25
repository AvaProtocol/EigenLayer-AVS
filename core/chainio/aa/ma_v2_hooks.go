package aa

import (
	"bytes"
	"fmt"
	"math/big"
	"sync"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// Permission hooks for Modular Account v2 session grants.
//
// A SessionGrant's Hooks entries are `bytes25 HookConfig ++ initData`, sliced
// at exactly those offsets by ModuleManagerInternals. The HookConfig layout
// mirrors ValidationConfig — module (20) ++ entityId (4) ++ flags (1) — but
// the flag byte means something different (ERC-6900 HookConfigLib):
//
//	bit 0 (1): hook type — 1 validation hook, 0 execution hook
//	bit 1 (2): execution hook has a post-hook
//	bit 2 (4): execution hook has a pre-hook
//
// Alchemy's stock permission modules divide the §6.3 grant screen between
// them: AllowlistModule enforces WHERE (targets/selectors, at validation) and
// HOW MUCH of an ERC-20 (spend limit, at execution); TimeRangeModule enforces
// HOW LONG (via ERC-4337 validation data, checked by the EntryPoint).
//
// The execution-hook half of the allowlist has a structural consequence: an
// account whose validation carries ANY execution hook refuses operations
// whose callData is not executeUserOp-wrapped (RequireUserOperationContext),
// because without user-op context the account cannot tell at execution which
// hooks should run. WrapExecuteUserOp does the wrapping, and every operation
// under such a grant needs it — including the one carrying the deferred
// install, since the hooks are live from the moment the install applies.
const (
	HookFlagValidation  byte = 1
	HookFlagExecHasPost byte = 2
	HookFlagExecHasPre  byte = 4
)

// Stock module addresses, deployed at the same address on every chain the MA
// v2 factory is (modular-account deployments/v2/Deployments.md; code presence
// verified on Sepolia). AllowlistModule is the v2.0.1 redeploy, matching the
// v2.0.1 source its encoding here was written against.
const (
	AllowlistModuleAddressHex = "0x00000000003e826473a313e600b5b9b791f5a59a"
	TimeRangeModuleAddressHex = "0x00000000000082B8e2012be914dFA4f62A0573eA"
)

// AllowlistModuleAddress returns the v2.0.1 AllowlistModule address.
func AllowlistModuleAddress() common.Address { return common.HexToAddress(AllowlistModuleAddressHex) }

// TimeRangeModuleAddress returns the TimeRangeModule address.
func TimeRangeModuleAddress() common.Address { return common.HexToAddress(TimeRangeModuleAddressHex) }

// selectorExecuteUserOp is IAccountExecute.executeUserOp(PackedUserOperation,
// bytes32) — the v0.7 EntryPoint special-cases callData beginning with it and
// hands the account the full operation as execution context.
var selectorExecuteUserOp = [4]byte{0x8d, 0xd7, 0x71, 0x2f}

// PackHookConfig builds the bytes25 HookConfig:
// module (20) ++ entityId (4, big-endian) ++ flags (1).
func PackHookConfig(module common.Address, entityID uint32, flags byte) [25]byte {
	// Identical layout to ValidationConfig; only the flag semantics differ.
	return PackValidationConfig(module, entityID, flags)
}

// AllowlistInput mirrors AllowlistModule.AllowlistInput. One entry per
// target; target zero means "any address" for the listed selectors.
type AllowlistInput struct {
	Target common.Address
	// HasSelectorAllowlist scopes the target to Selectors; false allows any
	// selector on the target.
	HasSelectorAllowlist bool
	// HasERC20SpendLimit marks Target as a token whose transfer/approve
	// amounts are capped by ERC20SpendLimit. Enforced by the module's
	// EXECUTION hook — installing a grant with a limit requires the exec-hook
	// entry (AllowlistExecHook) alongside the validation-hook entry, and
	// executeUserOp wrapping on every operation.
	HasERC20SpendLimit bool
	ERC20SpendLimit    *big.Int
	Selectors          [][4]byte
}

var (
	hooksArgsOnce     sync.Once
	allowlistDataArgs abi.Arguments
	timeRangeDataArgs abi.Arguments
	hooksArgsErr      error
)

func ensureHookABIs() error {
	hooksArgsOnce.Do(func() {
		allowlistInputType, err := abi.NewType("tuple[]", "", []abi.ArgumentMarshaling{
			{Name: "target", Type: "address"},
			{Name: "hasSelectorAllowlist", Type: "bool"},
			{Name: "hasERC20SpendLimit", Type: "bool"},
			{Name: "erc20SpendLimit", Type: "uint256"},
			{Name: "selectors", Type: "bytes4[]"},
		})
		if err != nil {
			hooksArgsErr = err
			return
		}
		uint32Type, err := abi.NewType("uint32", "", nil)
		if err != nil {
			hooksArgsErr = err
			return
		}
		uint48Type, err := abi.NewType("uint48", "", nil)
		if err != nil {
			hooksArgsErr = err
			return
		}
		allowlistDataArgs = abi.Arguments{{Type: uint32Type}, {Type: allowlistInputType}}
		timeRangeDataArgs = abi.Arguments{{Type: uint32Type}, {Type: uint48Type}, {Type: uint48Type}}
	})
	return hooksArgsErr
}

// abiAllowlistInput is the shape go-ethereum's ABI encoder expects for the
// tuple: field names must match the marshaling above with exported Go names.
type abiAllowlistInput struct {
	Target               common.Address `abi:"target"`
	HasSelectorAllowlist bool           `abi:"hasSelectorAllowlist"`
	HasERC20SpendLimit   bool           `abi:"hasERC20SpendLimit"`
	Erc20SpendLimit      *big.Int       `abi:"erc20SpendLimit"`
	Selectors            [][4]byte      `abi:"selectors"`
}

// PackAllowlistInstallData encodes AllowlistModule.onInstall's payload:
// abi.encode(uint32 entityId, AllowlistInput[] inputs).
func PackAllowlistInstallData(entityID uint32, inputs []AllowlistInput) ([]byte, error) {
	if err := ensureHookABIs(); err != nil {
		return nil, err
	}
	if len(inputs) == 0 {
		return nil, fmt.Errorf("an empty allowlist install allows nothing and masks a bug")
	}
	encoded := make([]abiAllowlistInput, len(inputs))
	for i, in := range inputs {
		limit := in.ERC20SpendLimit
		if limit == nil {
			limit = big.NewInt(0)
		}
		if in.HasERC20SpendLimit && limit.Sign() == 0 {
			return nil, fmt.Errorf("input %d: a zero ERC20 spend limit with the flag set caps the token at nothing", i)
		}
		selectors := in.Selectors
		if selectors == nil {
			selectors = [][4]byte{}
		}
		encoded[i] = abiAllowlistInput{
			Target:               in.Target,
			HasSelectorAllowlist: in.HasSelectorAllowlist,
			HasERC20SpendLimit:   in.HasERC20SpendLimit,
			Erc20SpendLimit:      limit,
			Selectors:            selectors,
		}
	}
	return allowlistDataArgs.Pack(entityID, encoded)
}

// PackTimeRangeInstallData encodes TimeRangeModule.onInstall's payload:
// abi.encode(uint32 entityId, uint48 validUntil, uint48 validAfter).
// validUntil zero means "no expiry" to the module — reject it here instead:
// every grant this system issues has an explicit expiry (master doc §6.5).
func PackTimeRangeInstallData(entityID uint32, validUntil, validAfter uint64) ([]byte, error) {
	if err := ensureHookABIs(); err != nil {
		return nil, err
	}
	const maxUint48 = uint64(1)<<48 - 1
	if validUntil == 0 {
		return nil, fmt.Errorf("validUntil 0 would mean no expiry; grants must expire")
	}
	if validUntil > maxUint48 || validAfter > maxUint48 {
		return nil, fmt.Errorf("time range overflows uint48")
	}
	if validUntil <= validAfter {
		return nil, fmt.Errorf("validUntil %d <= validAfter %d", validUntil, validAfter)
	}
	return timeRangeDataArgs.Pack(entityID, new(big.Int).SetUint64(validUntil), new(big.Int).SetUint64(validAfter))
}

// AllowlistValidationHook builds the hook entry enforcing the allowlist at
// validation, carrying the module's install data (which also seeds any ERC-20
// limits — state is shared with the exec hook, so it installs once, here).
func AllowlistValidationHook(entityID uint32, inputs []AllowlistInput) ([]byte, error) {
	data, err := PackAllowlistInstallData(entityID, inputs)
	if err != nil {
		return nil, err
	}
	config := PackHookConfig(AllowlistModuleAddress(), entityID, HookFlagValidation)
	return append(config[:], data...), nil
}

// AllowlistExecHook builds the pre-execution hook entry that enforces ERC-20
// spend limits. No install data — the validation-hook entry installed the
// module state; a second onInstall with the same inputs would just repeat it.
// (The module's post-hook path reverts NotImplemented, so pre only.)
func AllowlistExecHook(entityID uint32) []byte {
	config := PackHookConfig(AllowlistModuleAddress(), entityID, HookFlagExecHasPre)
	return config[:]
}

// TimeRangeValidationHook builds the hook entry that bounds the grant in
// time. Enforcement is by the EntryPoint: the hook returns
// validUntil/validAfter in its validation data.
func TimeRangeValidationHook(entityID uint32, validUntil, validAfter uint64) ([]byte, error) {
	data, err := PackTimeRangeInstallData(entityID, validUntil, validAfter)
	if err != nil {
		return nil, err
	}
	config := PackHookConfig(TimeRangeModuleAddress(), entityID, HookFlagValidation)
	return append(config[:], data...), nil
}

// WrapExecuteUserOp prefixes execution calldata with the executeUserOp
// selector, opting the operation into user-op context. Required for every
// operation under a grant that carries execution hooks; harmless overhead
// (a calldata re-read) otherwise.
func WrapExecuteUserOp(execCallData []byte) ([]byte, error) {
	if len(execCallData) < 4 {
		return nil, fmt.Errorf("execution calldata is %d bytes, shorter than a selector", len(execCallData))
	}
	return append(selectorExecuteUserOp[:], execCallData...), nil
}

// UnwrapExecuteUserOp strips a leading executeUserOp selector if present.
// The send path prepends that selector when a grant has execution hooks
// (WrapExecuteUserOp); bundler eth_getUserOperationByHash then returns the
// wrapped bytes. Callers that unpack execute/executeBatch must strip first.
// Unwrapped calldata is returned unchanged.
func UnwrapExecuteUserOp(callData []byte) []byte {
	if len(callData) >= 4 && bytes.Equal(callData[:4], selectorExecuteUserOp[:]) {
		return callData[4:]
	}
	return callData
}
