package taskengine

import (
	"context"
	"fmt"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

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
	verify := func(context.Context, common.Address, uint32) (bool, error) { return true, nil }

	require.NoError(t, VerifySupersededTeardown(context.Background(), db, verify, superseded, nil))
}

// The failure the spike surfaced: the operation mined and the entity survived.
func TestTeardownVerificationRejectsASurvivingEntity(t *testing.T) {
	db, superseded := teardownFixture(t)
	verify := func(context.Context, common.Address, uint32) (bool, error) { return false, nil }

	err := VerifySupersededTeardown(context.Background(), db, verify, superseded, nil)
	require.Error(t, err, "a surviving entity is live authority and must not pass silently")
	require.Contains(t, err.Error(), "survived its teardown")
	require.Contains(t, err.Error(), superseded.ID, "the error must name the policy to chase")
}

// Unknown is not cleared. A transient RPC failure must not read as success —
// that would report an entity revoked while it is still spendable.
func TestTeardownVerificationTreatsAReadFailureAsUnverified(t *testing.T) {
	db, superseded := teardownFixture(t)
	verify := func(context.Context, common.Address, uint32) (bool, error) {
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
	verify := func(context.Context, common.Address, uint32) (bool, error) { called = true; return true, nil }

	require.NoError(t, VerifySupersededTeardown(context.Background(), db, verify, nil, nil))
	require.False(t, called, "a first grant removes nothing, so nothing is read")
}
