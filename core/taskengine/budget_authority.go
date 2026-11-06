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

const BudgetRetiredGrantInstalledCode = "BUDGET_RETIRED_GRANT_INSTALLED"

func (n *Engine) warmBudgetAuthorityInventory(ctx context.Context, owner, wallet common.Address, chain int64) error {
	view, err := n.budgetBackend().View(ctx, chain, nil)
	if err != nil {
		return fmt.Errorf("%w: budget authority preflight: %w", ErrBudgetReadUnavailable, err)
	}
	defer view.Close()
	return view.CheckAuthorities(ctx, owner, wallet)
}

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
	var retired *BudgetPolicyError
	for _, candidate := range sortedBudgetAuthorityCandidates(candidates) {
		key := aa.PackModuleEntity(candidate.Module, candidate.Entity)
		if known[key] {
			continue
		}
		registered, err := aa.ModuleEntityRegisteredOnChain(ctx, v, wallet, candidate.Module, candidate.Entity)
		if err != nil {
			return fmt.Errorf("%w: unknown entity registration: %w", ErrBudgetReadUnavailable, err)
		}
		if registered {
			if candidate.Module == aa.SingleSignerValidationModuleAddress() && candidate.Entity >= aa.MinSessionEntityID && len(v.retiredBudgetSigners()) > 0 {
				signer, readErr := aa.EntitySignerOnChain(ctx, v, wallet, candidate.Entity)
				if readErr != nil {
					return fmt.Errorf("%w: retired authority signer: %w", ErrBudgetReadUnavailable, readErr)
				}
				if v.isRetiredBudgetSigner(signer) {
					if retired == nil {
						cleanup, cleanupErr := v.retiredBudgetGrantCleanup(ctx, wallet, candidate, signer)
						if cleanupErr != nil {
							return cleanupErr
						}
						retired = &BudgetPolicyError{Code: BudgetRetiredGrantInstalledCode, Detail: "This wallet still has a permission for a retired Ava signer. Remove the old permission with the owner wallet, then check again.", OnChainCleanup: cleanup}
					}
					continue
				}
			}
			return &BudgetPolicyError{Code: BudgetAuthorityUnknownCode, Detail: fmt.Sprintf("This wallet has a validation authority Ava doesn't recognize (module %s, entity %d). Remove it, then check again.", candidate.Module.Hex(), candidate.Entity)}
		}
	}
	if err := v.VerifyCanonical(ctx); err != nil {
		return err
	}
	if retired != nil {
		return retired
	}
	return nil
}

func (v *pinnedBudgetView) retiredBudgetSigners() []common.Address {
	if v.n.config == nil {
		return nil
	}
	return v.n.config.WalletBudgetRetiredSessionSigners[v.chainID]
}

func (v *pinnedBudgetView) isRetiredBudgetSigner(signer common.Address) bool {
	if signer == (common.Address{}) {
		return false
	}
	for _, retired := range v.retiredBudgetSigners() {
		if signer == retired {
			return true
		}
	}
	return false
}

func (v *pinnedBudgetView) retiredBudgetGrantCleanup(ctx context.Context, wallet common.Address, candidate budgetAuthorityCandidate, signer common.Address) (*OnChainRevokeCleanup, error) {
	if err := v.reserveRetiredSessionEntity(ctx, wallet, candidate.Entity); err != nil {
		return nil, fmt.Errorf("%w: retired entity reservation: %w", ErrBudgetReadUnavailable, err)
	}
	install, recoveryErr := v.recoverRetiredBudgetInstall(ctx, wallet, candidate, signer)
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: retired authority cleanup: %w", ErrBudgetReadUnavailable, err)
	}
	if recoveryErr == nil && install != nil {
		data, err := aa.SessionSignerUninstallFromInstall(candidate.Entity, install)
		if err == nil {
			return &OnChainRevokeCleanup{EntityID: candidate.Entity, Target: wallet, CallData: data, ChainID: v.chainID}, nil
		}
		recoveryErr = err
	}
	if recoveryErr != nil && v.n.logger != nil {
		v.n.logger.Debug("Retired grant uses empty-hook cleanup", "chain_id", v.chainID, "wallet", wallet.Hex(), "entity_id", candidate.Entity, "err", recoveryErr)
	}
	// Always clear registration and the SingleSigner slot, even if the
	// historical install cannot be recovered. Empty hook data intentionally
	// skips hook module storage, which is inert once registration is removed.
	data, err := aa.PackSessionSignerUninstall(candidate.Entity, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: retired authority cleanup: %w", ErrBudgetReadUnavailable, err)
	}
	return &OnChainRevokeCleanup{EntityID: candidate.Entity, Target: wallet, CallData: data, ChainID: v.chainID}, nil
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
