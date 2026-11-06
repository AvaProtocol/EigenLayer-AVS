package aa

import (
	"bytes"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// HistoricalSessionSignerInstall finds one unambiguous install for the given
// live signer and entity. Only self-calls are followed: another wallet's install
// in the same batch must never supply this wallet's hook teardown data.
// The caller must also establish that the transaction emitted the corresponding
// ValidationInstalled event on the canonical chain.
func HistoricalSessionSignerInstall(account common.Address, entity uint32, signer common.Address, call []byte) ([]byte, error) {
	if entity < MinSessionEntityID || signer == (common.Address{}) {
		return nil, fmt.Errorf("historical install requires a session entity and signer")
	}
	if err := ensureInstallABIs(); err != nil {
		return nil, err
	}
	expectedData, err := PackSingleSignerInstallData(entity, signer)
	if err != nil {
		return nil, err
	}
	expectedEntity := PackModuleEntity(SingleSignerValidationModuleAddress(), entity)
	var found []byte
	var visit func([]byte, int) error
	visit = func(data []byte, depth int) error {
		if len(data) < 4 {
			return nil
		}
		if depth > 4 {
			return fmt.Errorf("historical self-call nesting exceeds recovery limit")
		}
		method := installValidationABI.Methods["installValidation"]
		if bytes.Equal(data[:4], method.ID) {
			args, unpackErr := method.Inputs.Unpack(data[4:])
			if unpackErr != nil {
				return unpackErr
			}
			validationConfig := args[0].([25]byte)
			if !bytes.Equal(validationConfig[:24], expectedEntity[:]) || !bytes.Equal(args[2].([]byte), expectedData) {
				return nil
			}
			if found != nil {
				return fmt.Errorf("multiple matching historical installs")
			}
			found = append([]byte(nil), data...)
			return nil
		}
		targets, values, calls, unpackErr := UnpackExecuteCalldata(data)
		if unpackErr != nil {
			return nil // Unrelated operation, not a self-install container.
		}
		for i, nested := range calls {
			if targets[i] == account && values[i].Sign() == 0 {
				if err := visit(nested, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(call, 0); err != nil {
		return nil, err
	}
	if found != nil {
		if err := checkHistoricalSessionHooks(entity, found); err != nil {
			return nil, err
		}
	}
	return found, nil
}

// Recovery is not permission to call an arbitrary old hook with arbitrary
// teardown arguments. Only Ava's known hook formats, scoped to this entity,
// are recovered; other histories use the empty-hook removal instead.
func checkHistoricalSessionHooks(entity uint32, install []byte) error {
	hooks, err := DecodeInstallValidationHooks(install)
	if err != nil {
		return err
	}
	for _, hook := range hooks {
		if len(hook) < hookConfigLen {
			return fmt.Errorf("historical hook is shorter than its config")
		}
		module := common.BytesToAddress(hook[:20])
		if module != AllowlistModuleAddress() && module != NativeTokenLimitModuleAddress() && module != TimeRangeModuleAddress() {
			return fmt.Errorf("unsupported historical hook module")
		}
		expected := PackModuleEntity(module, entity)
		if !bytes.Equal(hook[:24], expected[:]) {
			return fmt.Errorf("historical hook names another entity")
		}
		data := hook[hookConfigLen:]
		if len(data) != 0 && (len(data) < 32 || new(big.Int).SetBytes(data[:32]).Cmp(new(big.Int).SetUint64(uint64(entity))) != 0) {
			return fmt.Errorf("historical hook data names another entity")
		}
	}
	return nil
}
