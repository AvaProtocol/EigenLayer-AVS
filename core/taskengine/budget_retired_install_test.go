package taskengine

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
)

func retiredInstallTransaction(t *testing.T, wallet, signer common.Address, entity uint32) (*types.Transaction, []byte) {
	t.Helper()
	hook, err := aa.TimeRangeValidationHook(entity, 1800000000, 0)
	require.NoError(t, err)
	install, err := aa.PackSessionSignerInstall(aa.SessionGrant{EntityID: entity, Signer: signer, Selectors: [][4]byte{{1, 2, 3, 4}}, Hooks: [][]byte{hook}})
	require.NoError(t, err)
	locator := userop.FallbackSignerLocator()
	encoded, err := userop.EncodeDeferredActionData(locator, 1800000000, install)
	require.NoError(t, err)
	ownerSignature := make([]byte, 65)
	ownerSignature[64] = 27
	signature, err := userop.WrapSignatureMAv2Deferred(encoded, ownerSignature, []byte{0xff, 0x00})
	require.NoError(t, err)
	nonce, err := userop.EncodeNonceMAv2(entity, userop.ValidationOptionGlobal|userop.ValidationOptionDeferredAction, 0)
	require.NoError(t, err)
	op := budgetHistoricalUserOp{Sender: wallet, Nonce: nonce, PreVerificationGas: new(big.Int), Signature: signature}
	data, err := budgetHistoricalEntryPointABI.Pack("handleOps", []budgetHistoricalUserOp{op}, common.Address{})
	require.NoError(t, err)
	entryPoint := common.HexToAddress(config.EntryPointV07AddressHex)
	return types.NewTx(&types.LegacyTx{To: &entryPoint, Data: data}), install
}

func TestBudgetRetiredInstallRecoversDeferredHookCleanup(t *testing.T) {
	v, rpc, owner := retiredBudgetFixture(t)
	tx, install := retiredInstallTransaction(t, rpc.wallet, rpc.signer, 99)
	log := &rpc.logs[0]
	log.BlockHash = budgetInventoryBlockHash(log.BlockNumber)
	log.TxHash = tx.Hash()
	rpc.historyTx = tx
	rpc.historyReceipt = &types.Receipt{Status: types.ReceiptStatusSuccessful, BlockNumber: new(big.Int).SetUint64(log.BlockNumber), BlockHash: log.BlockHash, TxHash: tx.Hash(), Logs: []*types.Log{log}}
	var refusal *BudgetPolicyError
	require.ErrorAs(t, v.CheckAuthorities(context.Background(), owner, rpc.wallet), &refusal)
	require.Equal(t, BudgetRetiredGrantInstalledCode, refusal.Code)
	want, err := aa.SessionSignerUninstallFromInstall(99, install)
	require.NoError(t, err)
	require.Equal(t, want, refusal.OnChainCleanup.CallData)
	// A warm inventory retains the install's provenance, without a history rescan.
	require.NotNil(t, readBudgetInventory(t, v, rpc.wallet).Candidates[0].Install)
	require.ErrorAs(t, v.CheckAuthorities(context.Background(), owner, rpc.wallet), &refusal)
	require.Equal(t, want, refusal.OnChainCleanup.CallData)
}

func TestBudgetRetiredInstallRebuildsV1CacheAndReusesV2ProvenanceAfterRestart(t *testing.T) {
	v, rpc, owner := retiredBudgetFixture(t)
	tx, install := retiredInstallTransaction(t, rpc.wallet, rpc.signer, 99)
	log := &rpc.logs[0]
	log.BlockHash, log.TxHash = budgetInventoryBlockHash(11), tx.Hash()
	rpc.historyTx = tx
	rpc.historyReceipt = &types.Receipt{Status: 1, BlockNumber: big.NewInt(11), BlockHash: log.BlockHash, TxHash: tx.Hash(), Logs: []*types.Log{log}}
	// The complete v1 checkpoint would suppress every historical log read,
	// leaving this candidate permanently without its install's provenance.
	old := budgetAuthorityInventory{Version: 1, ChainID: v.chainID, Wallet: rpc.wallet, Start: BudgetBlock{Number: 11, Hash: log.BlockHash}, Cursor: &v.block, Candidates: []budgetAuthorityCandidate{{Module: aa.SingleSignerValidationModuleAddress(), Entity: 99}}}
	raw, err := json.Marshal(old)
	require.NoError(t, err)
	oldKey := []byte(fmt.Sprintf("budgetauthority:v1:%d:%s", v.chainID, strings.ToLower(rpc.wallet.Hex())))
	require.NoError(t, v.n.db.Set(oldKey, raw))
	require.NoError(t, v.reserveRetiredSessionEntity(context.Background(), rpc.wallet, 500))
	var refusal *BudgetPolicyError
	require.ErrorAs(t, v.CheckAuthorities(context.Background(), owner, rpc.wallet), &refusal)
	want, err := aa.SessionSignerUninstallFromInstall(99, install)
	require.NoError(t, err)
	require.Equal(t, want, refusal.OnChainCleanup.CallData)
	cache := readBudgetInventory(t, v, rpc.wallet)
	require.EqualValues(t, 2, cache.Version)
	require.NotNil(t, cache.Candidates[0].Install)
	require.Equal(t, tx.Hash(), cache.Candidates[0].Install.Transaction)
	unchanged, err := v.n.db.GetKey(oldKey)
	require.NoError(t, err)
	require.Equal(t, raw, unchanged, "the derived v1 record is ignored, not migrated in place")
	history, queries, _ := budgetInventoryReadCounts(rpc)
	require.Positive(t, queries, "the v1 cursor must not suppress provenance backfill")
	// Restart with a provider that refuses any new backfill: the durable v2
	// provenance still produces full hook removal, not the empty-hook fallback.
	restarted := *v
	restarted.n = &Engine{db: v.n.db, config: v.n.config, smartWalletConfig: v.n.smartWalletConfig}
	rpc.historyError = true
	failed := uint64(0)
	rpc.logErrorFrom = &failed
	require.ErrorAs(t, restarted.CheckAuthorities(context.Background(), owner, rpc.wallet), &refusal)
	require.Equal(t, want, refusal.OnChainCleanup.CallData)
	historyAfter, queriesAfter, _ := budgetInventoryReadCounts(rpc)
	require.Equal(t, history, historyAfter)
	require.Equal(t, queries, queriesAfter)
	next, err := NextSessionEntityID(v.n.db, v.chainID, owner, rpc.wallet)
	require.NoError(t, err)
	require.EqualValues(t, 501, next, "cache invalidation never regresses permanent reservations")
}

func TestBudgetRetiredInstallUnavailableOrMismatchedFallsBack(t *testing.T) {
	for _, mode := range []string{"pruned", "wrong receipt", "missing event", "wrong signer", "wrong entrypoint", "ambiguous event"} {
		t.Run(mode, func(t *testing.T) {
			v, rpc, owner := retiredBudgetFixture(t)
			signer := rpc.signer
			if mode == "wrong signer" {
				signer = common.HexToAddress("0xcafe")
			}
			tx, _ := retiredInstallTransaction(t, rpc.wallet, signer, 99)
			if mode == "wrong entrypoint" {
				to := common.HexToAddress("0xabcd")
				tx = types.NewTx(&types.LegacyTx{To: &to, Data: tx.Data()})
			}
			log := &rpc.logs[0]
			log.BlockHash, log.TxHash = budgetInventoryBlockHash(11), tx.Hash()
			rpc.historyTx = tx
			rpc.historyReceipt = &types.Receipt{Status: 1, BlockNumber: big.NewInt(11), BlockHash: log.BlockHash, TxHash: tx.Hash(), Logs: []*types.Log{log}}
			switch mode {
			case "pruned":
				rpc.historyTx = nil
			case "wrong receipt":
				rpc.historyReceipt.BlockHash = common.HexToHash("0xdead")
			case "missing event":
				rpc.historyReceipt.Logs = nil
			case "ambiguous event":
				other := *log
				other.Index++
				rpc.historyReceipt.Logs = append(rpc.historyReceipt.Logs, &other)
			}
			var refusal *BudgetPolicyError
			require.ErrorAs(t, v.CheckAuthorities(context.Background(), owner, rpc.wallet), &refusal)
			require.Equal(t, BudgetRetiredGrantInstalledCode, refusal.Code)
			want, err := aa.PackSessionSignerUninstall(99, nil)
			require.NoError(t, err)
			require.Equal(t, want, refusal.OnChainCleanup.CallData)
		})
	}
}

func TestBudgetRetiredInstallDoesNotReadOtherWalletOperations(t *testing.T) {
	wallet, signer := common.HexToAddress("0x1111"), common.HexToAddress("0xbaba")
	tx, _ := retiredInstallTransaction(t, common.HexToAddress("0x2222"), signer, 99)
	install, err := retiredBudgetInstallInTransaction(tx, wallet, 99, signer)
	require.NoError(t, err)
	require.Nil(t, install)
}

func TestBudgetRetiredInstallRejectsTruncatedDeferredSignature(t *testing.T) {
	wallet, signer := common.HexToAddress("0x1111"), common.HexToAddress("0xbaba")
	nonce, err := userop.EncodeNonceMAv2(99, 3, 0)
	require.NoError(t, err)
	for _, signature := range [][]byte{nil, {0, 0, 0, 20}, {0xff, 0xff, 0xff, 0xff}} {
		op := budgetHistoricalUserOp{Sender: wallet, Nonce: nonce, PreVerificationGas: new(big.Int), Signature: signature}
		data, err := budgetHistoricalEntryPointABI.Pack("handleOps", []budgetHistoricalUserOp{op}, common.Address{})
		require.NoError(t, err)
		to := common.HexToAddress(config.EntryPointV07AddressHex)
		_, err = retiredBudgetInstallInTransaction(types.NewTx(&types.LegacyTx{To: &to, Data: data}), wallet, 99, signer)
		require.Error(t, err)
	}
}

// The standard ethclient decoders are exercised, not replaced with a fake
// history interface, so transaction and receipt JSON shapes stay covered.
func (f *budgetInventoryRPC) GetTransactionByHash(_ context.Context, hash common.Hash) (map[string]interface{}, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.historyTx == nil || f.historyTx.Hash() != hash {
		return nil, nil
	}
	encoded, err := f.historyTx.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var result map[string]interface{}
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil, err
	}
	result["blockNumber"], result["blockHash"] = hexutil.EncodeUint64(11), budgetInventoryBlockHash(11)
	return result, nil
}

func (f *budgetInventoryRPC) GetTransactionReceipt(_ context.Context, _ common.Hash) (*types.Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.historyReceipt, nil
}
