package taskengine

import (
	"context"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/stretchr/testify/require"
)

func retiredBudgetFixture(t *testing.T) (*pinnedBudgetView, *budgetInventoryRPC, common.Address) {
	t.Helper()
	v, rpc := newBudgetInventoryView(t, 20, 11, 3)
	rpc.signer = common.HexToAddress("0xbaba")
	rpc.registered = true
	log := budgetInstallLog(rpc, 11)
	log.Topics[1] = common.BytesToHash(aa.SingleSignerValidationModuleAddress().Bytes())
	rpc.logs = []types.Log{log}
	v.n.config = &config.Config{WalletBudgetRetiredSessionSigners: map[int64][]common.Address{v.chainID: {rpc.signer}}}
	return v, rpc, common.HexToAddress("0xb1")
}

func TestBudgetRetiredGrantRequiresOwnerRemovalWithoutPolicyRecord(t *testing.T) {
	v, rpc, owner := retiredBudgetFixture(t)
	err := v.CheckAuthorities(context.Background(), owner, rpc.wallet)
	var refusal *BudgetPolicyError
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, BudgetRetiredGrantInstalledCode, refusal.Code)
	require.NotNil(t, refusal.OnChainCleanup)
	want, err := aa.PackSessionSignerUninstall(99, nil)
	require.NoError(t, err)
	require.Equal(t, want, refusal.OnChainCleanup.CallData)
	require.Equal(t, rpc.wallet, refusal.OnChainCleanup.Target)
	require.Equal(t, v.chainID, refusal.OnChainCleanup.ChainID)
	require.EqualValues(t, 99, refusal.OnChainCleanup.EntityID)
	// The same cached inventory does not keep blocking once the owner's
	// removal is reflected in registration and module state.
	rpc.registered = false
	rpc.signer = common.Address{}
	require.NoError(t, v.CheckAuthorities(context.Background(), owner, rpc.wallet))
	// Even a direct install with an unused nonce cannot be reissued after
	// fallback removal: its inert hook storage belongs to the retired entity.
	next, err := NextFreeSessionEntityID(context.Background(), v.n.db, v.chainID, owner, rpc.wallet, nil)
	require.NoError(t, err)
	require.EqualValues(t, 100, next)
	otherChain, err := NextSessionEntityID(v.n.db, v.chainID+1, owner, rpc.wallet)
	require.NoError(t, err)
	require.EqualValues(t, 1, otherChain)
}

func TestBudgetRetiredGrantKeepsForeignAndUnconfiguredAuthoritiesUnknown(t *testing.T) {
	for _, mode := range []string{"foreign signer", "wrong chain", "unconfigured", "owner entity", "foreign module", "foreign alongside retired"} {
		t.Run(mode, func(t *testing.T) {
			v, rpc, owner := retiredBudgetFixture(t)
			switch mode {
			case "foreign signer":
				rpc.signer = common.HexToAddress("0xcafe")
			case "wrong chain":
				v.n.config.WalletBudgetRetiredSessionSigners = map[int64][]common.Address{v.chainID + 1: {rpc.signer}}
			case "unconfigured":
				v.n.config.WalletBudgetRetiredSessionSigners = nil
			case "owner entity":
				rpc.logs[0].Topics[2] = common.Hash{}
			case "foreign module":
				rpc.logs[0].Topics[1] = common.HexToHash("0x123")
			case "foreign alongside retired":
				foreign := budgetInstallLog(rpc, 12)
				foreign.Topics[1] = common.BytesToHash(common.HexToAddress("0xffffffffffffffffffffffffffffffffffffffff").Bytes())
				rpc.logs = append(rpc.logs, foreign) // Sorted after the retired candidate.
			}
			var refusal *BudgetPolicyError
			require.ErrorAs(t, v.CheckAuthorities(context.Background(), owner, rpc.wallet), &refusal)
			require.Equal(t, BudgetAuthorityUnknownCode, refusal.Code)
			require.Nil(t, refusal.OnChainCleanup)
		})
	}
}

func TestBudgetRetiredGrantSignerReadFailureFailsClosed(t *testing.T) {
	v, rpc, owner := retiredBudgetFixture(t)
	rpc.signerError = true
	require.ErrorIs(t, v.CheckAuthorities(context.Background(), owner, rpc.wallet), ErrBudgetReadUnavailable)
}

func TestBudgetRetiredGrantCancellationDoesNotReturnCleanup(t *testing.T) {
	v, rpc, _ := retiredBudgetFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cleanup, err := v.retiredBudgetGrantCleanup(ctx, rpc.wallet, budgetAuthorityCandidate{Module: aa.SingleSignerValidationModuleAddress(), Entity: 99}, rpc.signer)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, ErrBudgetReadUnavailable)
	require.Nil(t, cleanup)
}
