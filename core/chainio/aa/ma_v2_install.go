package aa

import (
	"fmt"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// Controller validation install for Modular Account v2.
//
// A stock SemiModularAccountBytecode trusts exactly one signer — the owner
// passed at creation. The controller's authority over a wallet is therefore
// an explicit, per-account grant: an installValidation self-call naming the
// controller as an additional validation entity. This file encodes that call.
//
// The encoding was established on-chain (Sepolia, avs-infra
// MA_v2_Authority_Model_And_Spike_Results.md §3.1) rather than from
// documentation: ValidationConfig is bytes25 = module address (20) ++
// entityId (uint32, 4) ++ flags (1), and the flag bits follow aa-sdk's
// serializeValidationConfig, which is NOT the intuitive order.

// SingleSignerValidationModuleAddressHex is Alchemy's audited
// SingleSignerValidationModule, deployed at the same address on every chain
// the MA v2 factory is (verified byte-identical across our chains).
const SingleSignerValidationModuleAddressHex = "0x00000000000099DE0BF6fA90dEB851E2A2df7d83"

// SingleSignerValidationModuleAddress returns the module address.
func SingleSignerValidationModuleAddress() common.Address {
	return common.HexToAddress(SingleSignerValidationModuleAddressHex)
}

// MinSessionEntityID is the lowest entity a session signer may occupy. Entity
// 0 is the account's fallback signer (the owner) and can never be reused.
//
// Entities are allocated PER GRANT, not per gateway — one SessionPolicy, one
// entity, one signer (avs-infra Smart_Wallet_MA_v2_Spend_Policy.md §6.7). That
// is not only a product choice. The deferred-action digest commits to the full
// nonce of the operation that will carry it, and the nonce key is derived from
// the entity id, so a fresh entity means a fresh key whose sequence is
// predictably zero — which is the entire reason the owner can sign the grant
// before the operation exists. Reusing one entity across grants makes the
// second grant's nonce unpredictable at signing time and the property
// collapses.
const MinSessionEntityID uint32 = 1

// ValidationConfig flag bits (aa-sdk serializeValidationConfig order).
const (
	// ValidationFlagUserOp lets the entity validate UserOperations.
	ValidationFlagUserOp byte = 1
	// ValidationFlagSignature lets the entity answer isValidSignature as the
	// account. The controller install deliberately EXCLUDES this: the
	// controller executes, it does not speak as the user.
	ValidationFlagSignature byte = 2
	// ValidationFlagGlobal applies the validation to any selector rather than
	// an enumerated set.
	ValidationFlagGlobal byte = 4
)

// PackValidationConfig builds the bytes25 ValidationConfig:
// module (20) ++ entityId (4, big-endian) ++ flags (1).
func PackValidationConfig(module common.Address, entityID uint32, flags byte) [25]byte {
	var out [25]byte
	copy(out[:20], module.Bytes())
	out[20] = byte(entityID >> 24)
	out[21] = byte(entityID >> 16)
	out[22] = byte(entityID >> 8)
	out[23] = byte(entityID)
	out[24] = flags
	return out
}

var (
	installArgsOnce      sync.Once
	installDataArgs      abi.Arguments
	installValidationABI abi.ABI
	installArgsErr       error
)

func ensureInstallABIs() error {
	installArgsOnce.Do(func() {
		uint32Type, err := abi.NewType("uint32", "", nil)
		if err != nil {
			installArgsErr = err
			return
		}
		addressType, err := abi.NewType("address", "", nil)
		if err != nil {
			installArgsErr = err
			return
		}
		installDataArgs = abi.Arguments{{Type: uint32Type}, {Type: addressType}}

		installValidationABI, installArgsErr = abi.JSON(strings.NewReader(`[
		  {"type":"function","name":"installValidation","stateMutability":"nonpayable",
		   "inputs":[{"name":"validationConfig","type":"bytes25"},
		             {"name":"selectors","type":"bytes4[]"},
		             {"name":"installData","type":"bytes"},
		             {"name":"hooks","type":"bytes[]"}]}
		]`))
	})
	return installArgsErr
}

// PackSingleSignerInstallData encodes the module's onInstall payload:
// abi.encode(uint32 entityId, address signer).
func PackSingleSignerInstallData(entityID uint32, signer common.Address) ([]byte, error) {
	if err := ensureInstallABIs(); err != nil {
		return nil, err
	}
	return installDataArgs.Pack(entityID, signer)
}

// PackInstallValidation encodes the installValidation self-call
// (selector 0x1bbf564c, asserted in tests against deployed bytecode).
func PackInstallValidation(config [25]byte, selectors [][4]byte, installData []byte, hooks [][]byte) ([]byte, error) {
	if err := ensureInstallABIs(); err != nil {
		return nil, err
	}
	if selectors == nil {
		selectors = [][4]byte{}
	}
	if hooks == nil {
		hooks = [][]byte{}
	}
	return installValidationABI.Pack("installValidation", config, selectors, installData, hooks)
}

// SessionGrant is one grant of authority on one account: which entity, which
// signer occupies it, and what that signer is allowed to do.
//
// This is the thing an owner authorizes, so every field is committed to by the
// deferred-action digest. The gateway cannot widen a grant after signing —
// changing any field here changes the digest and invalidates the signature.
type SessionGrant struct {
	// EntityID must be unique per account and >= MinSessionEntityID. It is
	// allocated by the gateway when a SessionPolicy is created.
	EntityID uint32

	// Signer is the session key that will sign operations for this grant.
	Signer common.Address

	// Selectors scopes the grant to specific function selectors. Mutually
	// exclusive with Global — a global validation never consults the selector
	// list, so setting both would produce a grant that is wider than it reads.
	Selectors [][4]byte

	// Global lets the entity validate ANY selector the account exposes,
	// including the account's own native functions. It must be set
	// explicitly: it used to be inferred from an empty Selectors, which made
	// the most dangerous grant the one you got by omission. See
	// AllowSelfAdministration.
	Global bool

	// Hooks are the encoded permission hooks (allowlist, spend cap, time
	// range) installed alongside the validation, each as
	// hookConfig ++ initData. They ride the SAME installValidation call, so
	// they are covered by the owner's signature rather than needing one of
	// their own.
	Hooks [][]byte

	// AllowSignatureValidation lets this entity answer isValidSignature AS the
	// account. Default false, and it should stay false for gateway-held
	// signers: the controller executes, it does not speak as the user. Set it
	// only for a grant that genuinely represents the owner.
	AllowSignatureValidation bool

	// AllowSelfAdministration acknowledges that a Global grant carrying no
	// execution hook can administer its own authority.
	//
	// A global validation may call the account's native functions, which is
	// how one-click self-revoke works (uninstallValidation, proven on Sepolia)
	// — but the same authority reaches installValidation, so such a grant can
	// escalate itself: add signature validation, widen its own hooks, or
	// install a second signer. An execution hook that rejects non-execute
	// selectors closes this (the allowlist hook does, also proven), which is
	// why a policied grant cannot touch its own authority.
	//
	// Production grants must therefore either be selector-scoped or carry an
	// execution hook. This flag exists so a spike or a deliberately
	// self-administering grant can still be built, loudly.
	AllowSelfAdministration bool
}

// HasExecutionHook reports whether any hook runs at execution rather than
// validation. Hook entries are hookConfig(25) ++ initData, and a clear
// HookFlagValidation bit in the config's flag byte means execution.
func (g SessionGrant) HasExecutionHook() bool {
	for _, h := range g.Hooks {
		if len(h) >= 25 && h[24]&HookFlagValidation == 0 {
			return true
		}
	}
	return false
}

// Validate reports why a grant cannot be installed, if it cannot.
func (g SessionGrant) Validate() error {
	if g.EntityID < MinSessionEntityID {
		return fmt.Errorf("entity id %d is reserved (entity 0 is the owner's fallback signer)", g.EntityID)
	}
	if g.Signer == (common.Address{}) {
		return fmt.Errorf("session signer is the zero address")
	}
	// Scope must be stated. Neither means a grant that validates nothing;
	// both means a grant whose selector list is silently ignored.
	if g.Global && len(g.Selectors) > 0 {
		return fmt.Errorf("grant is Global and also lists %d selectors; a global validation ignores the list, so this is wider than it reads", len(g.Selectors))
	}
	if !g.Global && len(g.Selectors) == 0 {
		return fmt.Errorf("grant has no scope: set Global or list Selectors")
	}
	if g.Global && !g.HasExecutionHook() && !g.AllowSelfAdministration {
		return fmt.Errorf(
			"refusing a global grant with no execution hook: it can call the account's own installValidation/uninstallValidation and escalate itself. " +
				"Scope it with Selectors, add an execution hook (e.g. aa.AllowlistExecHook), or set AllowSelfAdministration to accept this deliberately")
	}
	return nil
}

// Flags renders the grant's ValidationConfig flag byte.
func (g SessionGrant) Flags() byte {
	flags := ValidationFlagUserOp
	if g.Global {
		flags |= ValidationFlagGlobal
	}
	if g.AllowSignatureValidation {
		flags |= ValidationFlagSignature
	}
	return flags
}

// PackSessionSignerInstall encodes the installValidation self-call that grants
// this session signer its authority — the exact calldata an owner authorizes.
func PackSessionSignerInstall(grant SessionGrant) ([]byte, error) {
	if err := grant.Validate(); err != nil {
		return nil, err
	}
	installData, err := PackSingleSignerInstallData(grant.EntityID, grant.Signer)
	if err != nil {
		return nil, fmt.Errorf("packing SingleSignerValidationModule install data: %w", err)
	}
	config := PackValidationConfig(
		SingleSignerValidationModuleAddress(),
		grant.EntityID,
		grant.Flags(),
	)
	return PackInstallValidation(config, grant.Selectors, installData, grant.Hooks)
}
