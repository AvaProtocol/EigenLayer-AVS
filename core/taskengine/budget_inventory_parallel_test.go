package taskengine

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
)

func TestBudgetInventoryBackfillOverlapsFourReadsAndKeepsCompleteRanges(t *testing.T) {
	v, rpc, owner := parallelBudgetInventoryFixture(t)
	entered, release := make(chan uint64, 4), make(chan struct{})
	rpc.logWaiter = func(ctx context.Context, from uint64) error {
		if from < 12 {
			entered <- from
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- v.CheckAuthorities(ctx, owner, rpc.wallet) }()
	starts := make([]int, 0, 4)
	for i := 0; i < 4; i++ {
		select {
		case from := <-entered:
			starts = append(starts, int(from))
		case <-ctx.Done():
			t.Fatal("backfill did not overlap its first four reads")
		}
	}
	sort.Ints(starts)
	require.Equal(t, []int{0, 3, 6, 9}, starts)
	close(release)
	var refusal *BudgetPolicyError
	require.ErrorAs(t, <-result, &refusal)
	require.Equal(t, BudgetAuthorityUnknownCode, refusal.Code, "later-page installs cannot be skipped")
	require.EqualValues(t, 40, readBudgetInventory(t, v, rpc.wallet).Cursor.Number)
	rpc.mu.Lock()
	queries := append([][2]uint64(nil), rpc.queries...)
	rpc.mu.Unlock()
	sort.Slice(queries, func(i, j int) bool { return queries[i][0] < queries[j][0] })
	var next uint64
	for _, q := range queries {
		require.Equal(t, next, q[0])
		require.LessOrEqual(t, q[1]-q[0]+1, uint64(3))
		next = q[1] + 1
	}
	require.EqualValues(t, 41, next)
}

func TestBudgetInventoryParallelFailureResumesOnlyAfterCompletePrefix(t *testing.T) {
	v, rpc, owner := parallelBudgetInventoryFixture(t)
	failed := uint64(9)
	rpc.logErrorFrom = &failed
	require.ErrorIs(t, v.CheckAuthorities(context.Background(), owner, rpc.wallet), ErrBudgetReadUnavailable)
	require.EqualValues(t, 8, readBudgetInventory(t, v, rpc.wallet).Cursor.Number)
	rpc.logErrorFrom = nil
	rpc.queries = nil
	var refusal *BudgetPolicyError
	require.ErrorAs(t, v.CheckAuthorities(context.Background(), owner, rpc.wallet), &refusal)
	require.Equal(t, BudgetAuthorityUnknownCode, refusal.Code)
	for _, q := range rpc.queries {
		require.GreaterOrEqual(t, q[0], uint64(9), "a retry retains the complete finalized prefix")
	}
}

func TestBudgetInventoryParallelCancellationDoesNotSaveUnfinishedPages(t *testing.T) {
	v, rpc, owner := parallelBudgetInventoryFixture(t)
	entered := make(chan struct{}, 4)
	rpc.logWaiter = func(ctx context.Context, _ uint64) error {
		entered <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- v.CheckAuthorities(ctx, owner, rpc.wallet) }()
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("backfill did not start")
		}
	}
	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorIs(t, err, ErrBudgetReadUnavailable)
	case <-time.After(5 * time.Second):
		t.Fatal("backfill did not stop its readers")
	}
	require.Nil(t, readBudgetInventory(t, v, rpc.wallet).Cursor)
}

func parallelBudgetInventoryFixture(t *testing.T) (*pinnedBudgetView, *budgetInventoryRPC, common.Address) {
	t.Helper()
	v, rpc := newBudgetInventoryView(t, 40, 0, 3)
	rpc.registered = true
	rpc.logs = []types.Log{budgetInstallLog(rpc, 35)}
	return v, rpc, common.HexToAddress("0xb1")
}

func TestBudgetInventoryParallelReorgCannotPersistMixedForkPages(t *testing.T) {
	v, rpc, owner := parallelBudgetInventoryFixture(t)
	rpc.onLogQuery = func(from uint64) {
		if from != 3 {
			return
		}
		rpc.block.Hash = common.BytesToHash([]byte{0xbb, 40})
		rpc.blockHashes = make(map[uint64]common.Hash)
		for block := uint64(0); block < 40; block++ {
			rpc.blockHashes[block] = common.BytesToHash([]byte{0xbb, byte(block)})
		}
		log := budgetInstallLog(rpc, 1)
		log.BlockHash = rpc.blockHashes[1]
		rpc.logs = []types.Log{log}
		rpc.onLogQuery = nil
	}
	require.ErrorIs(t, v.CheckAuthorities(context.Background(), owner, rpc.wallet), ErrBudgetReadUnavailable)
	cache := readBudgetInventory(t, v, rpc.wallet)
	if cache.Cursor != nil {
		require.Equal(t, budgetInventoryBlockHash(cache.Cursor.Number), cache.Cursor.Hash, "never persist an old prefix under a new-fork checkpoint")
	}
	v.block = rpc.block
	var refusal *BudgetPolicyError
	require.ErrorAs(t, v.CheckAuthorities(context.Background(), owner, rpc.wallet), &refusal)
	require.Equal(t, BudgetAuthorityUnknownCode, refusal.Code, "a reorg rebuild must find the new earlier install")
}
