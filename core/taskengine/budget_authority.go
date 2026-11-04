package taskengine

import (
	"context"
	"fmt"
	"math/big"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// The pinned account emits these events for every validation install/uninstall.
// The event is an inventory candidate, never proof of current registration.
// ERC-6900 v0.8 IModularAccount / Alchemy ModularAccount v2.0.1.
var budgetValidationInstalled = crypto.Keccak256Hash([]byte("ValidationInstalled(address,uint32)"))

func (v *pinnedBudgetView) CheckAuthorities(ctx context.Context, owner, wallet common.Address) error {
	logs, err := v.chain.FilterLogs(ctx, ethereum.FilterQuery{
		FromBlock: big.NewInt(0), ToBlock: new(big.Int).SetUint64(v.block.Number),
		Addresses: []common.Address{wallet}, Topics: [][]common.Hash{{budgetValidationInstalled}},
	})
	if err != nil {
		return fmt.Errorf("%w: account validation inventory", ErrBudgetReadUnavailable)
	}
	known, err := v.knownBudgetAuthorities(owner, wallet)
	if err != nil {
		return err
	}
	seen := map[[24]byte]bool{}
	for _, log := range logs {
		if log.Removed || log.Address != wallet || len(log.Topics) != 3 || log.Topics[0] != budgetValidationInstalled || log.Topics[2].Big().BitLen() > 32 {
			return fmt.Errorf("%w: malformed validation inventory", ErrBudgetReadUnavailable)
		}
		module := common.BytesToAddress(log.Topics[1].Bytes())
		entity := uint32(log.Topics[2].Big().Uint64())
		key := aa.PackModuleEntity(module, entity)
		if known[key] || seen[key] {
			continue
		}
		seen[key] = true
		registered, err := aa.ModuleEntityRegisteredOnChain(ctx, v, wallet, module, entity)
		if err != nil {
			return fmt.Errorf("%w: unknown entity registration", ErrBudgetReadUnavailable)
		}
		if registered {
			return &BudgetPolicyError{Code: BudgetAuthorityUnknownCode, Detail: fmt.Sprintf("Remove the unrecognized validation authority %s, entity %d, in your wallet, then check again. Ava's runs remain paused.", module.Hex(), entity)}
		}
	}
	return v.VerifyCanonical(ctx)
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
