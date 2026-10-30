package aa

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// EntityModuleStorageCleared checks the modules actually named by the signed
// install. It does not infer revocation: only EntityRegisteredOnChain does that.
// Unknown modules are unverified, not silently assumed clean.
func EntityModuleStorageCleared(ctx context.Context, client ContractCaller, account common.Address, entity uint32, installCall []byte) (bool, error) {
	signer, err := EntitySignerOnChain(ctx, client, account, entity)
	if err != nil || signer != (common.Address{}) {
		return false, err
	}
	install, err := InstallValidationWithin(installCall)
	if err != nil {
		return false, err
	}
	hooks, err := DecodeInstallValidationHooks(install)
	if err != nil {
		return false, err
	}
	if err := ensureHookABIs(); err != nil {
		return false, err
	}
	for _, hook := range hooks {
		if len(hook) < hookConfigLen {
			return false, fmt.Errorf("malformed hook on entity %d", entity)
		}
		module := common.BytesToAddress(hook[:20])
		switch module {
		case AllowlistModuleAddress():
			if len(hook[hookConfigLen:]) == 0 { // stateless execution half
				continue
			}
			values, err := allowlistDataArgs.Unpack(hook[hookConfigLen:])
			if err != nil || len(values) != 2 {
				return false, fmt.Errorf("decoding allowlist residue targets: %v", err)
			}
			inputs := *abi.ConvertType(values[1], new([]abiAllowlistInput)).(*[]abiAllowlistInput)
			for _, input := range inputs {
				words := append(abiWord(new(big.Int).SetUint64(uint64(entity))), abiWord(new(big.Int).SetBytes(input.Target.Bytes()))...)
				words = append(words, abiWord(new(big.Int).SetBytes(account.Bytes()))...)
				clear, err := moduleWordsZero(ctx, client, module, "addressAllowlist(uint32,address,address)", words, 3)
				if err != nil || !clear {
					return false, err
				}
				clear, err = moduleWordsZero(ctx, client, module, "erc20SpendLimits(uint32,address,address)", words, 1)
				if err != nil || !clear {
					return false, err
				}
				for _, selector := range input.Selectors {
					args := abiWord(new(big.Int).SetUint64(uint64(entity)))
					args = append(args, common.RightPadBytes(selector[:], 32)...)
					args = append(args, abiWord(new(big.Int).SetBytes(input.Target.Bytes()))...)
					args = append(args, abiWord(new(big.Int).SetBytes(account.Bytes()))...)
					clear, err := moduleWordsZero(ctx, client, module, "selectorAllowlist(uint32,bytes4,address,address)", args, 1)
					if err != nil || !clear {
						return false, err
					}
				}
			}
		case TimeRangeModuleAddress():
			until, after, err := EntityTimeRangeOnChain(ctx, client, account, entity)
			if err != nil || until != 0 || after != 0 {
				return false, err
			}
		case NativeTokenLimitModuleAddress():
			remaining, err := ReadNativeSpendLimit(ctx, client, entity, account)
			if err != nil || remaining.Sign() != 0 {
				return false, err
			}
		default:
			return false, fmt.Errorf("no residue reader for module %s", module.Hex())
		}
	}
	return true, nil
}

func moduleWordsZero(ctx context.Context, client ContractCaller, module common.Address, signature string, args []byte, words int) (bool, error) {
	data := append(crypto.Keccak256([]byte(signature))[:4], args...)
	out, err := client.CallContract(ctx, ethereum.CallMsg{To: &module, Data: data}, nil)
	if err != nil {
		return false, err
	}
	if len(out) != words*32 {
		return false, fmt.Errorf("%s returned %d bytes, want %d", signature, len(out), words*32)
	}
	return new(big.Int).SetBytes(out).Sign() == 0, nil
}

// ModuleCleanupFromInstall builds direct account → module onUninstall calls.
// Unlike uninstallValidation, this works after the account's hook lists are
// gone. The owner must authorize the batch; a session must never execute it.
func ModuleCleanupFromInstall(entity uint32, installCall []byte) ([]Call, error) {
	install, err := InstallValidationWithin(installCall)
	if err != nil {
		return nil, err
	}
	hooks, err := DecodeInstallValidationHooks(install)
	if err != nil {
		return nil, err
	}
	bytesType, err := abi.NewType("bytes", "", nil)
	if err != nil {
		return nil, err
	}
	pack := func(module common.Address, payload []byte) (Call, error) {
		args, err := (abi.Arguments{{Type: bytesType}}).Pack(payload)
		return Call{Target: module, Value: big.NewInt(0), Data: append(crypto.Keccak256([]byte("onUninstall(bytes)"))[:4], args...)}, err
	}
	calls := make([]Call, 0, len(hooks)+1)
	seen := make(map[string]bool)
	for _, hook := range hooks {
		if len(hook) < hookConfigLen {
			return nil, fmt.Errorf("malformed hook on entity %d", entity)
		}
		payload, err := hookUninstallPayload(entity, hook)
		if err != nil {
			return nil, err
		}
		if len(payload) == 0 {
			continue
		}
		module := common.BytesToAddress(hook[:20])
		key := string(module.Bytes()) + string(payload)
		if seen[key] {
			continue
		}
		seen[key] = true
		call, err := pack(module, payload)
		if err != nil {
			return nil, err
		}
		calls = append(calls, call)
	}
	call, err := pack(SingleSignerValidationModuleAddress(), abiWord(new(big.Int).SetUint64(uint64(entity))))
	if err != nil {
		return nil, err
	}
	return append(calls, call), nil
}
