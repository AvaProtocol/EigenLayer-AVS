package taskengine

import (
	"bytes"
	"context"
	"fmt"
	"math/big"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// The pinned account emits these events for every validation install/uninstall.
// The event is an inventory candidate, never proof of current registration.
// ERC-6900 v0.8 IModularAccount / Alchemy ModularAccount v2.0.1.
var budgetValidationInstalled = crypto.Keccak256Hash([]byte("ValidationInstalled(address,uint32)"))

func (v *pinnedBudgetView) CheckAuthorities(ctx context.Context, owner, wallet common.Address) error {
	code, err := v.CodeAt(ctx, wallet)
	if err != nil {
		return fmt.Errorf("%w: account validation code: %w", ErrBudgetReadUnavailable, err)
	}
	if len(code) == 0 {
		// Counterfactual accounts have no installed validation state. Still
		// verify the snapshot before treating its absence as established.
		return v.VerifyCanonical(ctx)
	}
	candidates, err := v.budgetAuthorityCandidates(ctx, wallet, code)
	if err != nil {
		return err
	}
	known, err := v.knownBudgetAuthorities(owner, wallet)
	if err != nil {
		return err
	}
	for key, candidate := range candidates {
		if known[key] {
			continue
		}
		registered, err := aa.ModuleEntityRegisteredOnChain(ctx, v, wallet, candidate.Module, candidate.Entity)
		if err != nil {
			return fmt.Errorf("%w: unknown entity registration: %w", ErrBudgetReadUnavailable, err)
		}
		if registered {
			return &BudgetPolicyError{Code: BudgetAuthorityUnknownCode, Detail: fmt.Sprintf("Remove the unrecognized validation authority %s, entity %d, in your wallet, then check again. Ava's runs remain paused.", candidate.Module.Hex(), candidate.Entity)}
		}
	}
	return v.VerifyCanonical(ctx)
}

// Normal account creation begins with empty code/storage. Binary-searching
// code history gives a safe lower bound for that account's surviving state,
// including its constructor's install events. An EIP-7702 authorization can
// clear code without clearing storage, so its code history is NOT such a
// boundary: delegated accounts must retain the complete event history.
func (v *pinnedBudgetView) budgetAuthorityFromBlock(ctx context.Context, wallet common.Address, code []byte) (uint64, error) {
	if bytes.HasPrefix(code, []byte{0xef, 0x01, 0x00}) {
		return 0, nil
	}
	low, high := uint64(0), v.block.Number
	for low < high {
		mid := low + (high-low)/2
		historical, err := v.chain.CodeAt(ctx, wallet, new(big.Int).SetUint64(mid))
		if err != nil {
			return 0, fmt.Errorf("%w: account deployment history at block %d: %w", ErrBudgetReadUnavailable, mid, err)
		}
		if len(historical) == 0 {
			low = mid + 1
		} else {
			high = mid
		}
	}
	return low, nil
}

func (v *pinnedBudgetView) knownBudgetAuthorities(owner, wallet common.Address) (map[[24]byte]bool, error) {
	policies, err := ListSessionPolicies(v.n.db, v.chainID, owner)
	if err != nil {
		return nil, err
	}
	known := map[[24]byte]bool{}
	for _, p := range policies {
		if p.Runner != nil && *p.Runner == wallet && !p.AwaitingBudgetActivation {
			known[aa.PackModuleEntity(aa.SingleSignerValidationModuleAddress(), p.EntityID)] = true
		}
	}
	return known, nil
}
