package taskengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/dgraph-io/badger/v4"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

type budgetAuthorityCandidate struct {
	Module common.Address `json:"module"`
	Entity uint32         `json:"entity"`
}

// Derived inventory only: a candidate is never evidence of live authority.
// Start is hash-anchored; Cursor and Candidates cover complete finalized pages.
// Current registration is checked separately on every call, including cache hits.
type budgetAuthorityInventory struct {
	Version    uint8                      `json:"version"`
	ChainID    int64                      `json:"chainId"`
	Wallet     common.Address             `json:"wallet"`
	Start      BudgetBlock                `json:"start"`
	Cursor     *BudgetBlock               `json:"cursor,omitempty"`
	Candidates []budgetAuthorityCandidate `json:"candidates"`
}

func budgetAuthorityInventoryKey(chain int64, wallet common.Address) []byte {
	return []byte(fmt.Sprintf("budgetauthority:v1:%d:%s", chain, strings.ToLower(wallet.Hex())))
}

func (v *pinnedBudgetView) budgetAuthorityCandidates(ctx context.Context, wallet common.Address, code []byte) (map[[24]byte]budgetAuthorityCandidate, error) {
	release, err := v.n.budgetAuthorityLocks.acquire(ctx, budgetGateKey(v.chainID, common.Address{}, wallet))
	if err != nil {
		return nil, err
	}
	defer release()
	inventory, writable, err := v.loadBudgetAuthorityInventory(ctx, wallet)
	if err != nil {
		return nil, err
	}
	if inventory == nil {
		inventory, err = v.newBudgetAuthorityInventory(ctx, wallet, code)
		if err != nil {
			return nil, err
		}
		if writable {
			if err = v.saveBudgetAuthorityInventory(inventory); err != nil {
				return nil, err
			}
		}
	}
	candidates := make(map[[24]byte]budgetAuthorityCandidate, len(inventory.Candidates))
	for _, candidate := range inventory.Candidates {
		candidates[aa.PackModuleEntity(candidate.Module, candidate.Entity)] = candidate
	}
	if inventory.Cursor != nil && inventory.Cursor.Number == v.block.Number {
		return candidates, nil
	}
	if err = v.extendBudgetAuthorityInventory(ctx, inventory, candidates, writable); err != nil {
		return nil, err
	}
	return candidates, nil
}

func (v *pinnedBudgetView) loadBudgetAuthorityInventory(ctx context.Context, wallet common.Address) (*budgetAuthorityInventory, bool, error) {
	raw, err := v.n.db.GetKey(budgetAuthorityInventoryKey(v.chainID, wallet))
	if errors.Is(err, badger.ErrKeyNotFound) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: loading validation inventory: %w", ErrBudgetReadUnavailable, err)
	}
	var inventory budgetAuthorityInventory
	if json.Unmarshal(raw, &inventory) != nil || !inventory.valid(v.chainID, wallet) {
		// Invalid derived data is rebuilt, never interpreted as an empty scan.
		return nil, true, nil
	}
	anchor := inventory.Start
	if inventory.Cursor != nil {
		anchor = *inventory.Cursor
	}
	if anchor.Number > v.block.Number {
		// An older pinned view cannot use future candidates or regress the
		// durable cursor belonging to a newer view.
		return nil, false, nil
	}
	canonical, err := budgetBlockAt(ctx, v.chain, new(big.Int).SetUint64(anchor.Number))
	if err != nil {
		return nil, false, err
	}
	if canonical.Hash != anchor.Hash {
		return nil, true, nil // Reorg: rebuild both the boundary and inventory.
	}
	return &inventory, true, nil
}

func (c *budgetAuthorityInventory) valid(chain int64, wallet common.Address) bool {
	if c.Version != 1 || c.ChainID != chain || c.Wallet != wallet || c.Start.Hash == (common.Hash{}) {
		return false
	}
	if c.Cursor == nil {
		return len(c.Candidates) == 0
	}
	return c.Cursor.Hash != (common.Hash{}) && c.Cursor.Number >= c.Start.Number
}

func (v *pinnedBudgetView) newBudgetAuthorityInventory(ctx context.Context, wallet common.Address, code []byte) (*budgetAuthorityInventory, error) {
	from, err := v.budgetAuthorityFromBlock(ctx, wallet, code)
	if err != nil {
		return nil, err
	}
	start, err := budgetBlockAt(ctx, v.chain, new(big.Int).SetUint64(from))
	if err != nil {
		return nil, err
	}
	if err = v.VerifyCanonical(ctx); err != nil {
		return nil, err
	}
	return &budgetAuthorityInventory{Version: 1, ChainID: v.chainID, Wallet: wallet, Start: start}, nil
}

func (v *pinnedBudgetView) saveBudgetAuthorityInventory(inventory *budgetAuthorityInventory) error {
	raw, err := json.Marshal(inventory)
	if err == nil {
		err = v.n.db.Set(budgetAuthorityInventoryKey(v.chainID, inventory.Wallet), raw)
	}
	if err != nil {
		return fmt.Errorf("%w: storing validation inventory: %w", ErrBudgetReadUnavailable, err)
	}
	return nil
}

func (v *pinnedBudgetView) extendBudgetAuthorityInventory(ctx context.Context, inventory *budgetAuthorityInventory, candidates map[[24]byte]budgetAuthorityCandidate, writable bool) error {
	finalized, err := budgetBlockAtTag(ctx, v.chain, "finalized")
	if err != nil {
		return err
	}
	if finalized.Number > v.block.Number {
		finalized = v.block
	}
	from := inventory.Start.Number
	if inventory.Cursor != nil {
		from = inventory.Cursor.Number + 1 // Cursor < pinned block, checked above.
	}
	query := ethereum.FilterQuery{Addresses: []common.Address{inventory.Wallet}, Topics: [][]common.Hash{{budgetValidationInstalled}}}
	if from <= finalized.Number {
		query.FromBlock, query.ToBlock = new(big.Int).SetUint64(from), new(big.Int).SetUint64(finalized.Number)
		err = v.forEachBudgetLogPage(ctx, query, func(logs []types.Log, end uint64) error {
			for _, log := range logs {
				if candidateErr := addBudgetAuthorityCandidate(inventory.Wallet, candidates, log); candidateErr != nil {
					return candidateErr
				}
			}
			checkpoint, checkpointErr := budgetBlockAt(ctx, v.chain, new(big.Int).SetUint64(end))
			if checkpointErr != nil {
				return checkpointErr
			}
			if canonicalErr := v.VerifyCanonical(ctx); canonicalErr != nil {
				return canonicalErr
			}
			inventory.Cursor = &checkpoint
			inventory.Candidates = sortedBudgetAuthorityCandidates(candidates)
			if writable {
				return v.saveBudgetAuthorityInventory(inventory)
			}
			return nil
		})
		if err != nil {
			return err
		}
		if finalized.Number == v.block.Number {
			return nil
		}
		from = finalized.Number + 1
	}
	// Never persist the unfinalized tail. It is re-read through the pinned
	// head every time, so a reorg cannot hide a new install behind the cursor.
	query.FromBlock, query.ToBlock = new(big.Int).SetUint64(from), new(big.Int).SetUint64(v.block.Number)
	return v.forEachBudgetLog(ctx, query, func(log types.Log) error {
		return addBudgetAuthorityCandidate(inventory.Wallet, candidates, log)
	})
}

func addBudgetAuthorityCandidate(wallet common.Address, candidates map[[24]byte]budgetAuthorityCandidate, log types.Log) error {
	if log.Removed || log.Address != wallet || len(log.Topics) != 3 || log.Topics[0] != budgetValidationInstalled || log.Topics[2].Big().BitLen() > 32 {
		return fmt.Errorf("%w: malformed validation inventory", ErrBudgetReadUnavailable)
	}
	candidate := budgetAuthorityCandidate{Module: common.BytesToAddress(log.Topics[1].Bytes()), Entity: uint32(log.Topics[2].Big().Uint64())}
	candidates[aa.PackModuleEntity(candidate.Module, candidate.Entity)] = candidate
	return nil
}

func sortedBudgetAuthorityCandidates(candidates map[[24]byte]budgetAuthorityCandidate) []budgetAuthorityCandidate {
	result := make([]budgetAuthorityCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Module == result[j].Module {
			return result[i].Entity < result[j].Entity
		}
		return result[i].Module.Hex() < result[j].Module.Hex()
	})
	return result
}
