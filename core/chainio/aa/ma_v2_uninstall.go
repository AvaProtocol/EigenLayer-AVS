package aa

import (
	"fmt"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// Revocation for Modular Account v2 session grants.
//
// uninstallValidation(bytes24 moduleEntity, bytes uninstallData,
// bytes[] hookUninstallData), selector 0xb6b1ccfe. Like installValidation it
// is annotated "may be validated by a global validation" in the account
// source — which is why a BARE global session grant can revoke (or reinstall)
// itself, and why a policied grant cannot: the allowlist's execution hook
// reverts SpendingRequestNotAllowed for any selector that is not
// execute/executeBatch. Both behaviors carry receipts in the results doc
// §3.5. The owner always can, either as a UserOperation or as a plain
// transaction straight to the account (fallback direct-call validation).
//
// hookUninstallData ordering is the account's, not the install's:
// **validation hooks in STORED order (reverse of install — the hook set is a
// prepend-on-add linked list), then execution hooks (same reversal)**, one
// entry per installed hook or ArrayLengthMismatch. An entry routed to the
// wrong module does NOT fail the uninstall: onUninstall reverts are caught,
// flagged only in the ValidationUninstalled event, and the module state is
// silently stranded. Verify cleanup by reading module state back, not by the
// transaction succeeding.
//
// The uninstall payloads must be reconstructable at revoke time, which is
// one more reason master §7.4a stores the grant's install_call rather than
// re-deriving it: the AllowlistModule's uninstall data is the SAME
// (entityId, inputs) tuple it was installed with — decode it from the stored
// call, do not rebuild it from current code.

// PackModuleEntity builds the bytes24 ModuleEntity:
// module (20) ++ entityId (4, big-endian).
func PackModuleEntity(module common.Address, entityID uint32) [24]byte {
	var out [24]byte
	copy(out[:20], module.Bytes())
	out[20] = byte(entityID >> 24)
	out[21] = byte(entityID >> 16)
	out[22] = byte(entityID >> 8)
	out[23] = byte(entityID)
	return out
}

var (
	uninstallOnce   sync.Once
	uninstallABI    abi.ABI
	uninstallABIErr error
)

func ensureUninstallABI() error {
	uninstallOnce.Do(func() {
		uninstallABI, uninstallABIErr = abi.JSON(strings.NewReader(`[
		  {"type":"function","name":"uninstallValidation","stateMutability":"nonpayable",
		   "inputs":[{"name":"validationFunction","type":"bytes24"},
		             {"name":"uninstallData","type":"bytes"},
		             {"name":"hookUninstallData","type":"bytes[]"}]}
		]`))
	})
	return uninstallABIErr
}

// PackUninstallValidation encodes the uninstallValidation self-call.
// hookUninstallData must have exactly one entry per installed hook, in the
// account's stored order (see the package comment), or be empty to skip all
// hook cleanup. An empty entry skips that one hook's onUninstall.
func PackUninstallValidation(module common.Address, entityID uint32, uninstallData []byte, hookUninstallData [][]byte) ([]byte, error) {
	if err := ensureUninstallABI(); err != nil {
		return nil, err
	}
	if hookUninstallData == nil {
		hookUninstallData = [][]byte{}
	}
	for i, entry := range hookUninstallData {
		if entry == nil {
			hookUninstallData[i] = []byte{}
		}
	}
	return uninstallABI.Pack("uninstallValidation", PackModuleEntity(module, entityID), uninstallData, hookUninstallData)
}

// PackSingleSignerUninstallData encodes SingleSignerValidationModule's
// onUninstall payload: abi.encode(uint32 entityId). The module zeroes the
// signer for that entity.
func PackSingleSignerUninstallData(entityID uint32) ([]byte, error) {
	if err := ensureHookABIs(); err != nil {
		return nil, err
	}
	// Single uint32 argument — reuse the time-range args' first element.
	uint32Only := abi.Arguments{timeRangeDataArgs[0]}
	return uint32Only.Pack(entityID)
}

// PackTimeRangeUninstallData encodes TimeRangeModule's onUninstall payload:
// abi.encode(uint32 entityId) — note NOT the install tuple; the module only
// needs the entity to delete its range.
func PackTimeRangeUninstallData(entityID uint32) ([]byte, error) {
	return PackSingleSignerUninstallData(entityID)
}

// PackSessionSignerUninstall encodes the revocation of a session grant
// installed by PackSessionSignerInstall: the SingleSignerValidationModule
// entity plus per-hook cleanup payloads supplied by the caller in the
// account's stored hook order.
func PackSessionSignerUninstall(entityID uint32, hookUninstallData [][]byte) ([]byte, error) {
	if entityID < MinSessionEntityID {
		return nil, fmt.Errorf("entity id %d is reserved (entity 0 is the owner's fallback signer)", entityID)
	}
	uninstallData, err := PackSingleSignerUninstallData(entityID)
	if err != nil {
		return nil, err
	}
	return PackUninstallValidation(SingleSignerValidationModuleAddress(), entityID, uninstallData, hookUninstallData)
}
