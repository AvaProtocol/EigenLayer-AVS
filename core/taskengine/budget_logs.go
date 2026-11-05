package taskengine

import (
	"context"
	"fmt"
	"math/big"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
)

// forEachBudgetLog scans complete inclusive ranges without exceeding the
// configured provider limit. A missing page is an error, never an empty
// inventory or evidence that an owner operation did not reach the chain.
func (v *pinnedBudgetView) forEachBudgetLog(ctx context.Context, query ethereum.FilterQuery, visit func(types.Log) error) error {
	return v.forEachBudgetLogPage(ctx, query, func(logs []types.Log, _ uint64) error {
		for _, log := range logs {
			if err := visit(log); err != nil {
				return err
			}
		}
		return nil
	})
}

func (v *pinnedBudgetView) forEachBudgetLogPage(ctx context.Context, query ethereum.FilterQuery, visit func([]types.Log, uint64) error) error {
	if query.FromBlock == nil || query.ToBlock == nil || !query.FromBlock.IsUint64() || !query.ToBlock.IsUint64() || query.FromBlock.Cmp(query.ToBlock) > 0 {
		return fmt.Errorf("%w: invalid budget log range", ErrBudgetReadUnavailable)
	}
	blockRange := v.budgetLogBlockRange()
	from, to := query.FromBlock.Uint64(), query.ToBlock.Uint64()
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: budget log scan: %w", ErrBudgetReadUnavailable, err)
		}
		end := to
		if blockRange-1 < to-from {
			end = from + blockRange - 1
		}
		query.FromBlock, query.ToBlock = new(big.Int).SetUint64(from), new(big.Int).SetUint64(end)
		logs, err := v.chain.FilterLogs(ctx, query)
		if err != nil {
			return fmt.Errorf("%w: budget logs at blocks %d-%d: %w", ErrBudgetReadUnavailable, from, end, err)
		}
		if err := visit(logs, end); err != nil {
			return err
		}
		if end == to {
			return nil
		}
		from = end + 1
	}
}

func (v *pinnedBudgetView) budgetLogBlockRange() uint64 {
	if v.n != nil {
		if sw := v.n.ResolveSmartWalletConfig(v.chainID); sw != nil && sw.BudgetLogBlockRange > 0 {
			return sw.BudgetLogBlockRange
		}
	}
	return config.DefaultBudgetLogBlockRange
}
