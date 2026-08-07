package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// A replace batch that mines proves the operation executed, not that the
// uninstall did anything: the account catches a hook module's onUninstall
// revert and strands the state. On Sepolia the two outcomes were
// indistinguishable from a receipt — 601,275 gas with the entity still holding
// its signer and full spend cap, against 636,341 gas and a clean teardown.
//
// So these tests pin the one rule that follows: never report an entity revoked
// on the strength of a transaction.

func teardownFixture(t *testing.T) (storage.Storage, *model.SessionPolicy) {
	t.Helper()
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })

	owner := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	runner := common.HexToAddress("0x209eb31c199bEB4c386eF83CF442DE1a00667a1F")
	return db, &model.SessionPolicy{
		ID: "01supersededaaaaaaaaaaaaaa", Owner: &owner, Runner: &runner,
		ChainID: testPolicyChain, EntityID: 4, Status: model.SessionPolicyRevoked,
	}
}

func TestTeardownVerificationAcceptsAClearedEntity(t *testing.T) {
	db, superseded := teardownFixture(t)
	verify := func(context.Context, int64, common.Address, uint32) (bool, error) { return true, nil }

	require.NoError(t, VerifySupersededTeardown(context.Background(), db, verify, superseded, nil))
}

// The failure the spike surfaced: the operation mined and the entity survived.
func TestTeardownVerificationRejectsASurvivingEntity(t *testing.T) {
	db, superseded := teardownFixture(t)
	verify := func(context.Context, int64, common.Address, uint32) (bool, error) { return false, nil }

	err := VerifySupersededTeardown(context.Background(), db, verify, superseded, nil)
	require.Error(t, err, "a surviving entity is live authority and must not pass silently")
	require.Contains(t, err.Error(), "survived its teardown")
	require.Contains(t, err.Error(), superseded.ID, "the error must name the policy to chase")
}

// Unknown is not cleared. A transient RPC failure must not read as success —
// that would report an entity revoked while it is still spendable.
func TestTeardownVerificationTreatsAReadFailureAsUnverified(t *testing.T) {
	db, superseded := teardownFixture(t)
	verify := func(context.Context, int64, common.Address, uint32) (bool, error) {
		return false, fmt.Errorf("rpc unavailable")
	}

	err := VerifySupersededTeardown(context.Background(), db, verify, superseded, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "rpc unavailable")
}

// A process with no chain client skips verification rather than assuming it
// passed. Silence is honest; a fabricated success is not.
func TestTeardownVerificationSkipsWithoutAChainClient(t *testing.T) {
	db, superseded := teardownFixture(t)
	require.NoError(t, VerifySupersededTeardown(context.Background(), db, nil, superseded, nil))
}

// Nothing was superseded — nothing to verify.
func TestTeardownVerificationIgnoresAFirstGrant(t *testing.T) {
	db, _ := teardownFixture(t)
	called := false
	verify := func(context.Context, int64, common.Address, uint32) (bool, error) { called = true; return true, nil }

	require.NoError(t, VerifySupersededTeardown(context.Background(), db, verify, nil, nil))
	require.False(t, called, "a first grant removes nothing, so nothing is read")
}

// Explicit revoke rebuilds uninstallValidation from the stored install call
// (#717 AC3). That is what onChainCleanupRequired used to report into the void.
func TestBuildOnChainRevokeCleanupDerivesUninstallFromInstall(t *testing.T) {
	install, err := aa.PackSessionSignerInstall(aa.SessionGrant{
		EntityID: 4,
		Signer:   common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB"),
		Global:   true,
		Hooks:    [][]byte{aa.AllowlistExecHook(4)},
	})
	require.NoError(t, err)

	owner := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	runner := common.HexToAddress("0x209eb31c199bEB4c386eF83CF442DE1a00667a1F")
	policy := &model.SessionPolicy{
		ID: "01revokecleanupaaaaaaaaaa", Owner: &owner, Runner: &runner,
		ChainID: testPolicyChain, EntityID: 4, Status: model.SessionPolicyRevoked,
		Grant: &model.SessionGrantAuthorization{InstallCall: install, AppliedAt: 1},
	}

	cleanup, err := BuildOnChainRevokeCleanup(policy)
	require.NoError(t, err)
	require.Equal(t, uint32(4), cleanup.EntityID)
	require.Equal(t, runner, cleanup.Target)
	require.Equal(t, int64(testPolicyChain), cleanup.ChainID)
	require.NotEmpty(t, cleanup.CallData)

	// Same bytes SessionSignerUninstallFromInstall would produce — the product
	// path must not invent its own packing.
	want, err := aa.SessionSignerUninstallFromInstall(4, install)
	require.NoError(t, err)
	require.Equal(t, want, cleanup.CallData)
}

func TestBuildOnChainRevokeCleanupUnwrapsAReplaceBatch(t *testing.T) {
	// A grant that replaced another stores InstallCall as executeBatch
	// [install(new), uninstall(prior)]. Cleanup for THIS grant must still
	// tear down its own entity, peeling the install out of the batch.
	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	runner := common.HexToAddress("0x209eb31c199bEB4c386eF83CF442DE1a00667a1F")
	installNew, err := aa.PackSessionSignerInstall(aa.SessionGrant{
		EntityID: 2, Signer: signer, Global: true, Hooks: [][]byte{aa.AllowlistExecHook(2)},
	})
	require.NoError(t, err)
	installPrior, err := aa.PackSessionSignerInstall(aa.SessionGrant{
		EntityID: 1, Signer: signer, Global: true, Hooks: [][]byte{aa.AllowlistExecHook(1)},
	})
	require.NoError(t, err)
	uninstallPrior, err := aa.SessionSignerUninstallFromInstall(1, installPrior)
	require.NoError(t, err)

	batch, err := aa.PackExecuteBatchMAv2([]aa.Call{
		{Target: runner, Data: installNew},
		{Target: runner, Data: uninstallPrior},
	})
	require.NoError(t, err)

	owner := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	policy := &model.SessionPolicy{
		ID: "01revokebatchaaaaaaaaaaaa", Owner: &owner, Runner: &runner,
		ChainID: testPolicyChain, EntityID: 2, Status: model.SessionPolicyRevoked,
		Grant: &model.SessionGrantAuthorization{InstallCall: batch, AppliedAt: 1},
	}

	cleanup, err := BuildOnChainRevokeCleanup(policy)
	require.NoError(t, err)
	require.Equal(t, uint32(2), cleanup.EntityID, "cleanup targets this grant's entity, not the prior in the batch")
	// Must equal uninstall of the install peel, not of the prior entity.
	want, err := aa.SessionSignerUninstallFromInstall(2, installNew)
	require.NoError(t, err)
	require.Equal(t, want, cleanup.CallData)
	// And must not match entity 1's uninstall.
	notPrior, err := aa.SessionSignerUninstallFromInstall(1, installPrior)
	require.NoError(t, err)
	require.NotEqual(t, notPrior, cleanup.CallData)
}

func TestTornDownEntitiesAreExcludedFromReplaceBatchAndCleanup(t *testing.T) {
	engine, db, ownerKey, owner, wallet := newPolicyTestEngine(t)
	user := &model.User{Address: owner}

	first, _ := grantOn(t, engine, ownerKey, owner, wallet)
	require.NoError(t, MarkSessionGrantApplied(db, first, "0xfirst"))
	require.NoError(t, MarkEntityTornDown(db, testPolicyChain, owner, wallet, first.EntityID))

	// Next prepare must not try to uninstall an already-cleared entity.
	prepared, err := engine.PrepareSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "TradingBot",
		Permissions: testPermissions(),
	})
	require.NoError(t, err)
	require.Empty(t, prepared.Supersedes, "torn-down entity is not a teardown target")

	// Explicit cleanup refuses once TornDownAt is set.
	stored, err := engine.GetSessionPolicyByID(user, testPolicyChain, wallet, first.ID)
	// first is still usable until revoke — mark applied+torn on usable, then revoke
	require.NoError(t, err)
	// Soft-revoke the first for cleanup API shape
	_, cleanupReq, cleanup, err := engine.RevokeSessionPolicyByID(user, testPolicyChain, wallet, first.ID)
	require.NoError(t, err)
	require.False(t, cleanupReq, "already torn down: no cleanup payload")
	require.Nil(t, cleanup)
	_ = stored
}

func TestVerifySupersededTeardownMarksTornDownAt(t *testing.T) {
	db, superseded := teardownFixture(t)
	// Fixture has revoked status but no AppliedAt — set applied so mark path runs.
	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	superseded.SessionSigner = &signer
	superseded.Grant = &model.SessionGrantAuthorization{
		InstallCall: []byte{0x01}, AppliedAt: 1, OwnerSignature: make([]byte, 65),
		CarrierNonce: big.NewInt(1), Deadline: 1785541743,
	}
	require.NoError(t, StoreSessionPolicy(db, superseded))

	verify := func(context.Context, int64, common.Address, uint32) (bool, error) { return true, nil }
	require.NoError(t, VerifySupersededTeardown(context.Background(), db, verify, superseded, nil))

	all, err := ListSessionPolicies(db, testPolicyChain, *superseded.Owner)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.True(t, all[0].Grant.TornDown())
}
