package aa

import (
	"context"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// IModularAccountView.getValidationData from the ERC-6900 v0.8 interface used
// by Modular Account v2.0.1. Read the account's registration, not SingleSigner's
// storage: a failing module cleanup can leave the latter after revocation.
var validationViewABI = mustValidationViewABI()

func mustValidationViewABI() abi.ABI {
	parsed, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getValidationData","stateMutability":"view","inputs":[{"name":"validationFunction","type":"bytes24"}],"outputs":[{"name":"data","type":"tuple","components":[{"name":"validationFlags","type":"uint8"},{"name":"validationHooks","type":"bytes25[]"},{"name":"executionHooks","type":"bytes25[]"},{"name":"selectors","type":"bytes4[]"}]}]}]`))
	if err != nil {
		panic(err) // static ABI, a programming error
	}
	return parsed
}

type validationView struct {
	ValidationFlags uint8
	ValidationHooks [][25]byte
	ExecutionHooks  [][25]byte
	Selectors       [][4]byte
}

// EntityRegisteredOnChain returns true for any remaining registration state.
// Empty/malformed RPC replies are errors, never proof that authority is gone.
func EntityRegisteredOnChain(ctx context.Context, client ContractCaller, account common.Address, entity uint32) (bool, error) {
	if client == nil {
		return false, fmt.Errorf("no chain client")
	}
	moduleEntity := PackModuleEntity(SingleSignerValidationModuleAddress(), entity)
	data, err := validationViewABI.Pack("getValidationData", moduleEntity)
	if err != nil {
		return false, err
	}
	out, err := client.CallContract(ctx, ethereum.CallMsg{To: &account, Data: data}, nil)
	if err != nil {
		return false, fmt.Errorf("reading registration of entity %d on %s: %w", entity, account.Hex(), err)
	}
	values, err := validationViewABI.Unpack("getValidationData", out)
	if err != nil || len(values) != 1 {
		return false, fmt.Errorf("invalid registration reply for entity %d on %s: %v", entity, account.Hex(), err)
	}
	view := *abi.ConvertType(values[0], new(validationView)).(*validationView)
	return view.ValidationFlags != 0 || len(view.ValidationHooks) != 0 || len(view.ExecutionHooks) != 0 || len(view.Selectors) != 0, nil
}
