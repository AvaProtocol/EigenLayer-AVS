package taskengine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
)

func readBudgetInventory(t *testing.T, v *pinnedBudgetView, wallet common.Address) budgetAuthorityInventory {
	t.Helper()
	raw, err := v.n.db.GetKey(budgetAuthorityInventoryKey(v.chainID, wallet))
	require.NoError(t, err)
	var inventory budgetAuthorityInventory
	require.NoError(t, json.Unmarshal(raw, &inventory))
	return inventory
}

func budgetInventoryReadCounts(f *budgetInventoryRPC) (int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	historical := 0
	for _, read := range f.codeReads {
		if _, ok := read.Number(); ok {
			historical++
		}
	}
	return historical, len(f.queries), f.registrationCalls
}

func TestBudgetInventoryWarmCheckAndRestartReuseFinalizedHistory(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		name := "factory account"
		if delegated {
			name = "delegated EOA"
		}
		t.Run(name, func(t *testing.T) {
			v, f := newBudgetInventoryView(t, 20, 11, 3)
			if delegated {
				f.code = append([]byte{0xef, 0x01, 0x00}, common.HexToAddress("0x123").Bytes()...)
			}
			f.logs = []types.Log{budgetInstallLog(f, 11)}
			owner := common.HexToAddress("0xb1")
			require.NoError(t, v.CheckAuthorities(context.Background(), owner, f.wallet))
			oldHistory, oldQueries, oldRegistration := budgetInventoryReadCounts(f)
			cached := readBudgetInventory(t, v, f.wallet)
			require.EqualValues(t, 20, cached.Cursor.Number)
			require.Len(t, cached.Candidates, 1)
			// A new Engine has no process-local inventory state. The persisted
			// prefix must still avoid deployment lookup and history scans.
			restarted := *v
			restarted.n = &Engine{db: v.n.db, smartWalletConfig: v.n.smartWalletConfig}
			f.historyError = true
			require.NoError(t, restarted.CheckAuthorities(context.Background(), owner, f.wallet))
			history, queries, registered := budgetInventoryReadCounts(f)
			require.Equal(t, oldHistory, history)
			require.Equal(t, oldQueries, queries)
			require.Equal(t, oldRegistration+1, registered, "registration is never cached")
			f.registered = true
			err := restarted.CheckAuthorities(context.Background(), owner, f.wallet)
			var refusal *BudgetPolicyError
			require.ErrorAs(t, err, &refusal)
			require.Equal(t, BudgetAuthorityUnknownCode, refusal.Code)
		})
	}
}

func TestBudgetInventoryRescansUnfinalizedTailAndExtendsOnlyNewBlocks(t *testing.T) {
	v, f := newBudgetInventoryView(t, 20, 11, 3)
	finalized := uint64(18)
	f.finalized = &finalized
	owner := common.HexToAddress("0xb1")
	require.NoError(t, v.CheckAuthorities(context.Background(), owner, f.wallet))
	cached := readBudgetInventory(t, v, f.wallet)
	require.EqualValues(t, 18, cached.Cursor.Number)
	_, oldQueries, _ := budgetInventoryReadCounts(f)
	// Replace the unfinalized fork with one that installs an authority.
	f.block.Hash = common.HexToHash("0xeeee")
	v.block = f.block
	f.logs = []types.Log{budgetInstallLog(f, 19)}
	f.registered = true
	err := v.CheckAuthorities(context.Background(), owner, f.wallet)
	var refusal *BudgetPolicyError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, BudgetAuthorityUnknownCode, refusal.Code)
	f.mu.Lock()
	require.Equal(t, [][2]uint64{{19, 20}}, f.queries[oldQueries:])
	f.mu.Unlock()
	cached = readBudgetInventory(t, v, f.wallet)
	require.Empty(t, cached.Candidates, "unfinalized candidates are not persisted")
	require.EqualValues(t, 18, cached.Cursor.Number)
	_, oldQueries, _ = budgetInventoryReadCounts(f)
	f.block = BudgetBlock{Number: 23, Hash: budgetInventoryBlockHash(23)}
	v.block = f.block
	finalized = 21
	f.registered = false
	require.NoError(t, v.CheckAuthorities(context.Background(), owner, f.wallet))
	f.mu.Lock()
	require.Equal(t, [][2]uint64{{19, 21}, {22, 23}}, f.queries[oldQueries:])
	f.mu.Unlock()
	cached = readBudgetInventory(t, v, f.wallet)
	require.EqualValues(t, 21, cached.Cursor.Number)
	require.Len(t, cached.Candidates, 1)
}

func TestBudgetInventoryFailedWarmupResumesCompleteFinalizedPages(t *testing.T) {
	v, f := newBudgetInventoryView(t, 20, 11, 3)
	failedPage := uint64(14)
	f.logErrorFrom = &failedPage
	f.logs = []types.Log{budgetInstallLog(f, 11)}
	owner := common.HexToAddress("0xb1")
	require.ErrorIs(t, v.CheckAuthorities(context.Background(), owner, f.wallet), ErrBudgetReadUnavailable)
	cached := readBudgetInventory(t, v, f.wallet)
	require.EqualValues(t, 13, cached.Cursor.Number)
	require.Len(t, cached.Candidates, 1)
	oldHistory, oldQueries, _ := budgetInventoryReadCounts(f)
	f.logErrorFrom = nil
	f.historyError = true
	f.registered = true
	err := v.CheckAuthorities(context.Background(), owner, f.wallet)
	var refusal *BudgetPolicyError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, BudgetAuthorityUnknownCode, refusal.Code, "earlier-page candidates survive a failed scan")
	history, _, _ := budgetInventoryReadCounts(f)
	require.Equal(t, oldHistory, history)
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Equal(t, [][2]uint64{{14, 16}, {17, 19}, {20, 20}}, f.queries[oldQueries:])
}

func TestBudgetInventoryKeepsKnownCandidatesForLaterPolicyChanges(t *testing.T) {
	v, f := newBudgetInventoryView(t, 20, 11, 3)
	owner := common.HexToAddress("0xb1")
	log := budgetInstallLog(f, 11)
	log.Topics[1] = common.BytesToHash(aa.SingleSignerValidationModuleAddress().Bytes())
	f.logs = []types.Log{log}
	f.registered = true
	policy := &model.SessionPolicy{ID: "known", Owner: &owner, Runner: &f.wallet, ChainID: v.chainID, EntityID: 99, Status: model.SessionPolicyRevoked}
	require.NoError(t, StoreSessionPolicy(v.n.db, policy))
	require.NoError(t, v.CheckAuthorities(context.Background(), owner, f.wallet))
	_, oldQueries, registrations := budgetInventoryReadCounts(f)
	require.Zero(t, registrations)
	require.NoError(t, v.n.db.Delete(SessionPolicyKey(v.chainID, owner, policy.ID)))
	err := v.CheckAuthorities(context.Background(), owner, f.wallet)
	var refusal *BudgetPolicyError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, BudgetAuthorityUnknownCode, refusal.Code)
	_, queries, registrations := budgetInventoryReadCounts(f)
	require.Equal(t, oldQueries, queries)
	require.Equal(t, 1, registrations)
}

func TestBudgetInventoryRebuildsReorgedAndMalformedCache(t *testing.T) {
	for _, mode := range []string{"reorg", "invalid JSON", "wrong chain", "wrong wallet"} {
		t.Run(mode, func(t *testing.T) {
			v, f := newBudgetInventoryView(t, 20, 11, 3)
			owner := common.HexToAddress("0xb1")
			require.NoError(t, v.CheckAuthorities(context.Background(), owner, f.wallet))
			oldHistory, oldQueries, _ := budgetInventoryReadCounts(f)
			switch mode {
			case "reorg":
				f.block.Hash = common.HexToHash("0xdddd")
				v.block = f.block
			case "invalid JSON":
				require.NoError(t, v.n.db.Set(budgetAuthorityInventoryKey(v.chainID, f.wallet), []byte("broken")))
			default:
				cached := readBudgetInventory(t, v, f.wallet)
				if mode == "wrong chain" {
					cached.ChainID = 8453
				} else {
					cached.Wallet = common.HexToAddress("0xa2")
				}
				raw, err := json.Marshal(cached)
				require.NoError(t, err)
				require.NoError(t, v.n.db.Set(budgetAuthorityInventoryKey(v.chainID, f.wallet), raw))
			}
			f.logs = []types.Log{budgetInstallLog(f, 12)}
			f.registered = true
			err := v.CheckAuthorities(context.Background(), owner, f.wallet)
			var refusal *BudgetPolicyError
			require.ErrorAs(t, err, &refusal)
			require.Equal(t, BudgetAuthorityUnknownCode, refusal.Code)
			history, _, _ := budgetInventoryReadCounts(f)
			require.Greater(t, history, oldHistory)
			f.mu.Lock()
			require.Equal(t, [2]uint64{11, 13}, f.queries[oldQueries])
			f.mu.Unlock()
		})
	}
}

func TestBudgetInventoryFailedCheckpointReadBlocksCacheHit(t *testing.T) {
	v, f := newBudgetInventoryView(t, 20, 11, 3)
	owner := common.HexToAddress("0xb1")
	require.NoError(t, v.CheckAuthorities(context.Background(), owner, f.wallet))
	badBlock := uint64(20)
	f.blockReadError = &badBlock
	require.ErrorIs(t, v.CheckAuthorities(context.Background(), owner, f.wallet), ErrBudgetReadUnavailable)
}

func TestBudgetInventoryOldPinnedViewCannotOverwriteNewerCheckpoint(t *testing.T) {
	v, f := newBudgetInventoryView(t, 20, 11, 3)
	owner := common.HexToAddress("0xb1")
	require.NoError(t, v.CheckAuthorities(context.Background(), owner, f.wallet))
	key := budgetAuthorityInventoryKey(v.chainID, f.wallet)
	before, err := v.n.db.GetKey(key)
	require.NoError(t, err)
	older := *v
	older.block = BudgetBlock{Number: 18, Hash: budgetInventoryBlockHash(18)}
	require.NoError(t, older.CheckAuthorities(context.Background(), owner, f.wallet))
	after, err := v.n.db.GetKey(key)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

type failingBudgetInventoryStorage struct{ storage.Storage }

func (s failingBudgetInventoryStorage) Set(key, value []byte) error {
	if strings.HasPrefix(string(key), "budgetauthority:") {
		return errors.New("cache storage unavailable")
	}
	return s.Storage.Set(key, value)
}

func TestBudgetInventoryStorageFailureIsNotAnEmptyInventory(t *testing.T) {
	v, f := newBudgetInventoryView(t, 20, 11, 3)
	v.n.db = failingBudgetInventoryStorage{Storage: v.n.db}
	require.ErrorIs(t, v.CheckAuthorities(context.Background(), common.HexToAddress("0xb1"), f.wallet), ErrBudgetReadUnavailable)
}

func TestBudgetInventoryConcurrentChecksShareOnlyHistory(t *testing.T) {
	v, f := newBudgetInventoryView(t, 20, 11, 3)
	f.logs = []types.Log{budgetInstallLog(f, 11)}
	var wait sync.WaitGroup
	errCh := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errCh <- v.CheckAuthorities(context.Background(), common.HexToAddress("0xb1"), f.wallet)
		}()
	}
	wait.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
	_, queries, registered := budgetInventoryReadCounts(f)
	require.Equal(t, 4, queries, "only one request warms the history")
	require.Equal(t, 8, registered, "every request checks current registration")
}
