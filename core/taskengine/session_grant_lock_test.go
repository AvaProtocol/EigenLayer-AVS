package taskengine

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// The runner lock is not reentrant, so anything reachable from inside it must
// not take it again.
//
// dropClearedTeardownTargets runs inside PrepareSessionGrant, which submit
// calls while already holding the lock. When it finds a candidate the owner
// cleared out of band it records TornDownAt — and if that write takes the lock
// itself, submit wedges the shard for that (chain, owner, runner) forever:
// every later grant, revoke and confirmation for the wallet blocks behind it.
//
// It survives casual testing because prepare usually marks the entity first, so
// submit's re-prepare finds nothing left to mark. The window is an entity
// cleared BETWEEN prepare and submit — which is exactly what an owner running
// the onChainCleanup payload does.
func TestDropClearedTeardownTargetsDoesNotRetakeTheRunnerLock(t *testing.T) {
	chainID := int64(testPolicyChain)
	owner := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	runner := common.HexToAddress("0x0643f9d156e2CE584825511C548649F3289619c3")

	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })
	seedUsablePolicy(t, db, owner, runner, "01lockdeadlockaaaaaaaaaaaa", 7)

	policies, err := ListSessionPolicies(db, chainID, owner)
	require.NoError(t, err)
	require.Len(t, policies, 1)

	// Stand where submit stands: holding the runner's write lock.
	lock := sessionAuthorityLock(chainID, owner, runner)
	lock.Lock()
	defer lock.Unlock()

	clearedOnChain := func(context.Context, int64, common.Address, uint32) (bool, error) {
		return true, nil
	}

	done := make(chan error, 1)
	go func() {
		_, err := dropClearedTeardownTargets(context.Background(), db, chainID, owner, runner,
			policies, clearedOnChain)
		done <- err
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("dropClearedTeardownTargets blocked while the runner lock was held — " +
			"submit calls this under that lock, so this deadlocks the shard permanently")
	}
}
