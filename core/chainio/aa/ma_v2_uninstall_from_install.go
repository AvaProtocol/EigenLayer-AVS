package aa

import (
	"fmt"
)

// Deriving a grant's teardown from the call that created it.
//
// A grant's hooks cannot be torn down from current code. Each hook module's
// onUninstall payload is the SAME (entityId, inputs) tuple it was INSTALLED
// with, so rebuilding it from today's permission structs would silently
// diverge the moment a grant's shape changes — and the divergence does not
// announce itself. That is why model.SessionPolicy retains Grant.InstallCall:
// it is the only faithful record of what a given entity actually carries.
//
// Getting this wrong produces a SUCCESSFUL transaction that clears nothing.
// The account catches onUninstall reverts, flags them only in the
// ValidationUninstalled event, and strands the module state. Proven on Sepolia
// while spiking #717: an identical batch mined at 601,275 gas with
// success=true and left the prior entity holding both its signer and its full
// 100-token spend cap, because two teardown entries were misrouted. The
// corrected payload cleared both.
//
// Consequences, both non-negotiable for callers:
//
//   - Derive from the stored install call, never from live config.
//   - Treat a mined transaction as no evidence of teardown. Read the module
//     state back before reporting an entity revoked.

// SessionSignerUninstallFromInstall builds the uninstallValidation calldata
// that reverses installCall — the calldata a grant was created with.
//
// hookUninstallData must carry one entry per installed hook in the ACCOUNT's
// STORED order. The account keeps hooks as a prepend-on-add list, so stored
// order is the REVERSE of install order. A hook that carried install data is
// torn down with that same data; one that carried none (the allowlist's
// execution-hook entry, whose module state belongs to its validation-hook
// entry) takes an empty slot.
//
// Wrong ordering does not error here or on chain. It mines and does nothing.
func SessionSignerUninstallFromInstall(entityID uint32, installCall []byte) ([]byte, error) {
	hooks, err := DecodeInstallValidationHooks(installCall)
	if err != nil {
		return nil, fmt.Errorf("recovering hooks for entity %d: %w", entityID, err)
	}

	// Reverse into the account's stored order, keeping each hook's own install
	// data as its teardown payload.
	teardown := make([][]byte, 0, len(hooks))
	for i := len(hooks) - 1; i >= 0; i-- {
		entry := hooks[i]
		if len(entry) < hookConfigLen {
			return nil, fmt.Errorf("hook %d of entity %d is %d bytes, shorter than a hook config",
				i, entityID, len(entry))
		}
		// Everything after the 25-byte config is the module's install data,
		// which is exactly what its onUninstall expects back. An entry that is
		// config-only yields nil, not an empty non-nil slice — the ABI encodes
		// both as zero-length bytes, and nil states the intent.
		if data := entry[hookConfigLen:]; len(data) > 0 {
			teardown = append(teardown, append([]byte(nil), data...))
		} else {
			teardown = append(teardown, nil)
		}
	}

	return PackSessionSignerUninstall(entityID, teardown)
}

// hookConfigLen is the packed HookConfig width: module (20) ++ entityId (4) ++
// flags (1). See PackHookConfig.
const hookConfigLen = 25

// DecodeInstallValidationHooks recovers the hooks array from installValidation
// calldata, in INSTALL order.
//
// Exposed separately because callers other than teardown need it — verifying
// that an entity carries the hook set a stored policy claims, for instance,
// which is the only honest way to confirm a revoke landed.
func DecodeInstallValidationHooks(installCall []byte) ([][]byte, error) {
	if err := ensureInstallABIs(); err != nil {
		return nil, err
	}
	method, ok := installValidationABI.Methods["installValidation"]
	if !ok {
		return nil, fmt.Errorf("installValidation is missing from the ABI")
	}
	if len(installCall) < 4 {
		return nil, fmt.Errorf("install call is %d bytes, shorter than a selector", len(installCall))
	}
	if string(installCall[:4]) != string(method.ID) {
		return nil, fmt.Errorf("install call selector is 0x%x, not installValidation (0x%x)",
			installCall[:4], method.ID)
	}
	args, err := method.Inputs.Unpack(installCall[4:])
	if err != nil {
		return nil, fmt.Errorf("decoding installValidation: %w", err)
	}
	if len(args) != 4 {
		return nil, fmt.Errorf("installValidation decoded to %d arguments, want 4", len(args))
	}
	hooks, ok := args[3].([][]byte)
	if !ok {
		return nil, fmt.Errorf("installValidation hooks decoded to %T, not [][]byte", args[3])
	}
	return hooks, nil
}
