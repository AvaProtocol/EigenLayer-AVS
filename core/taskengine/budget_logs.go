package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"sync"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
)

// Backfill only: overlap a bounded number of slow provider reads, but deliver
// complete pages in ascending order to the existing canonical checkpoint logic.
// Warm checks and owner-operation reconciliation keep their sequential path.
func (v *pinnedBudgetView) forEachBudgetInventoryLogPage(ctx context.Context, query ethereum.FilterQuery, visit func([]types.Log, uint64) error) error {
	const concurrency = 4
	if query.FromBlock == nil || query.ToBlock == nil || !query.FromBlock.IsUint64() || !query.ToBlock.IsUint64() || query.FromBlock.Cmp(query.ToBlock) > 0 {
		return fmt.Errorf("%w: invalid budget log range", ErrBudgetReadUnavailable)
	}
	from, to, blockRange := query.FromBlock.Uint64(), query.ToBlock.Uint64(), v.budgetLogBlockRange()
	if (to-from)/blockRange < concurrency {
		return v.forEachBudgetLogPage(ctx, query, visit)
	}
	type pageResult struct {
		logs []types.Log
		err  error
	}
	type page struct {
		end    uint64
		result chan pageResult
	}
	for {
		last := false
		err := func() error {
			scanCtx, cancel := context.WithCancel(ctx)
			var workers sync.WaitGroup
			defer workers.Wait()
			defer cancel() // Cancel outstanding reads before waiting on every exit.
			pages := make([]page, 0, concurrency)
			for i := 0; i < concurrency; i++ {
				start, end := from, to
				if blockRange-1 < to-from {
					end = from + blockRange - 1
				}
				p := page{end: end, result: make(chan pageResult, 1)}
				pages = append(pages, p)
				q := query
				q.FromBlock, q.ToBlock = new(big.Int).SetUint64(start), new(big.Int).SetUint64(end)
				workers.Add(1)
				go func() {
					defer workers.Done()
					logs, err := v.chain.FilterLogs(scanCtx, q)
					if err != nil {
						err = fmt.Errorf("%w: budget logs at blocks %d-%d: %w", ErrBudgetReadUnavailable, start, end, err)
					}
					p.result <- pageResult{logs: logs, err: err}
				}()
				if end == to {
					last = true
					break
				}
				from = end + 1
			}
			for _, p := range pages {
				if err := scanCtx.Err(); err != nil {
					return fmt.Errorf("%w: budget log scan: %w", ErrBudgetReadUnavailable, err)
				}
				select {
				case result := <-p.result:
					if result.err != nil {
						return result.err
					}
					if err := visit(result.logs, p.end); err != nil {
						return err
					}
				case <-scanCtx.Done():
					return fmt.Errorf("%w: budget log scan: %w", ErrBudgetReadUnavailable, scanCtx.Err())
				}
			}
			return nil
		}()
		if err != nil || last {
			return err
		}
	}
}

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
