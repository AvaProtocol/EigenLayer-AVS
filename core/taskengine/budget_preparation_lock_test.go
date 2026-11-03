package taskengine

import (
	"context"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestBudgetPreparationLocksIsolateOwnersAndHonorCancellation(t *testing.T) {
	var locks budgetPreparationLockSet
	owner, other := common.HexToAddress("0x1234"), common.HexToAddress("0x5678")
	release, err := locks.acquire(context.Background(), owner)
	require.NoError(t, err)
	// Another owner is never serialized behind this owner's RPC pricing.
	releaseOther, err := locks.acquire(context.Background(), other)
	require.NoError(t, err)
	releaseOther()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = locks.acquire(ctx, owner)
	require.ErrorIs(t, err, context.Canceled)
	waiting, cancelWaiting := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		unlock, err := locks.acquire(waiting, owner)
		if unlock != nil {
			unlock()
		}
		done <- err
	}()
	require.Eventually(t, func() bool {
		locks.mu.Lock()
		defer locks.mu.Unlock()
		return locks.owners[owner].users == 2
	}, time.Second, time.Millisecond)
	cancelWaiting()
	require.ErrorIs(t, <-done, context.Canceled)
	release()
	locks.mu.Lock()
	require.Empty(t, locks.owners)
	locks.mu.Unlock()
}

func TestBudgetPreparationDoesNotHoldExecutionShard(t *testing.T) {
	f := newBudgetPrepareFixture(t)
	shard := f.n.executionMutex("budget-prepare:" + f.user.Address.Hex())
	shard.Lock()
	done := make(chan error, 1)
	go func() { _, err := f.n.PrepareSessionPolicy(f.user, f.in); done <- err }()
	select {
	case err := <-done:
		shard.Unlock()
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		shard.Unlock()
		<-done
		t.Fatal("preparation blocked on an unrelated execution shard")
	}
}
