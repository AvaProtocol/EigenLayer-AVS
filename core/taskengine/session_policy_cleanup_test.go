package taskengine

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

func readCleanupFixture(t *testing.T) (*Engine, *model.SessionPolicy) {
	t.Helper()
	n, db, _, owner, wallet := newPolicyTestEngine(t)
	policy := &model.SessionPolicy{
		ID: model.GenerateID(), Owner: &owner, Runner: &wallet,
		ChainID: testPolicyChain, EntityID: 7, Status: model.SessionPolicyRevoked,
		Grant: &model.SessionGrantAuthorization{AppliedAt: 1, InstallCall: []byte{1}},
	}
	require.NoError(t, StoreSessionPolicy(db, policy))
	return n, policy
}

func TestSessionPolicyCleanupReadDoesNotWaitForAuthorityLock(t *testing.T) {
	n, policy := readCleanupFixture(t)
	lock := sessionAuthorityLock(policy.ChainID, *policy.Owner, *policy.Runner)
	lock.Lock()
	done := make(chan error, 1)
	verify := func(context.Context, int64, common.Address, uint32) (bool, error) { return true, nil }
	go func() { done <- n.refreshSessionPolicyCleanup(context.Background(), policy, verify) }()
	select {
	case err := <-done:
		lock.Unlock()
		require.ErrorContains(t, err, "permission update in progress")
		require.True(t, policy.Grant.NeedsOnChainCleanup())
	case <-time.After(time.Second):
		lock.Unlock()
		<-done
		t.Fatal("a policy read waited on the authority lock")
	}
	require.NoError(t, n.refreshSessionPolicyCleanup(context.Background(), policy, verify))
	require.True(t, policy.Grant.TornDown())
}

func TestSessionPolicyCleanupReadPreservesConcurrentGrantUpdates(t *testing.T) {
	n, policy := readCleanupFixture(t)
	verify := func(context.Context, int64, common.Address, uint32) (bool, error) {
		latest, err := n.GetSessionPolicyByID(&model.User{Address: *policy.Owner}, policy.ChainID, *policy.Runner, policy.ID)
		require.NoError(t, err)
		latest.Grant.AppliedUserOpHash = "0xupdated-while-reading"
		latest.Grant.InstallCall = []byte{2}
		latest.Justification = "new metadata"
		require.NoError(t, StoreSessionPolicy(n.db, latest))
		return true, nil
	}
	require.NoError(t, n.refreshSessionPolicyCleanup(context.Background(), policy, verify))
	require.True(t, policy.Grant.TornDown())
	require.Equal(t, "0xupdated-while-reading", policy.Grant.AppliedUserOpHash)
	require.Equal(t, []byte{2}, policy.Grant.InstallCall)
	require.Equal(t, "new metadata", policy.Justification)
}

func TestSessionPolicyCleanupReadHonorsCancellation(t *testing.T) {
	n, policy := readCleanupFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	verify := func(context.Context, int64, common.Address, uint32) (bool, error) {
		called = true
		return true, nil
	}
	require.ErrorIs(t, n.refreshSessionPolicyCleanup(ctx, policy, verify), context.Canceled)
	require.False(t, called)
	require.True(t, policy.Grant.NeedsOnChainCleanup())

	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	verify = func(ctx context.Context, _ int64, _ common.Address, _ uint32) (bool, error) {
		<-ctx.Done()
		return false, ctx.Err()
	}
	err := n.refreshSessionPolicyCleanup(ctx, policy, verify)
	require.True(t, errors.Is(err, context.DeadlineExceeded))
	require.True(t, policy.Grant.NeedsOnChainCleanup())
}

func TestSessionPolicyCleanupReadDoesNotFallBackToAnotherChain(t *testing.T) {
	n, policy := readCleanupFixture(t)
	n.sessionChainReads = true
	policy.ChainID = 999999
	require.ErrorIs(t, n.RefreshSessionPolicyCleanup(context.Background(), policy), ErrSessionChainNotServed)
	require.True(t, policy.Grant.NeedsOnChainCleanup())
}
