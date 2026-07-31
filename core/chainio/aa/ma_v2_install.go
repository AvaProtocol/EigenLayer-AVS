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

// ControllerEntityID is where the controller is installed. Entity 0 is the
// account's fallback signer (the owner) and cannot be reused.
const ControllerEntityID uint32 = 1

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

// PackControllerInstall is the exact grant this system asks an owner to
// authorize: SingleSignerValidationModule at entity 1 naming the controller,
// global + userOp validation and deliberately NOT signature validation, no
// selector scoping, no hooks (permission hooks ride here later — master doc
// §5 step 2).
func PackControllerInstall(controller common.Address) ([]byte, error) {
	if controller == (common.Address{}) {
		return nil, fmt.Errorf("controller address is zero")
	}
	installData, err := PackSingleSignerInstallData(ControllerEntityID, controller)
	if err != nil {
		return nil, fmt.Errorf("packing SingleSignerValidationModule install data: %w", err)
	}
	config := PackValidationConfig(
		SingleSignerValidationModuleAddress(),
		ControllerEntityID,
		ValidationFlagGlobal|ValidationFlagUserOp,
	)
	return PackInstallValidation(config, nil, installData, nil)
}
