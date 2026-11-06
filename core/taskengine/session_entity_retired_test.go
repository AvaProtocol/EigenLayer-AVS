package taskengine

import (
	"bytes"
	"context"
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestRetiredSessionEntityReservationIsMonotonicAndChainScoped(t *testing.T) {
	v, rpc, owner := retiredBudgetFixture(t)
	var workers sync.WaitGroup
	failures := make(chan error, 10)
	for i := uint32(1); i <= 10; i++ {
		workers.Add(1)
		go func(entity uint32) {
			defer workers.Done()
			failures <- v.reserveRetiredSessionEntity(context.Background(), rpc.wallet, entity)
		}(i)
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	require.NoError(t, v.reserveRetiredSessionEntity(context.Background(), rpc.wallet, 2))
	got, err := NextSessionEntityID(v.n.db, v.chainID, owner, rpc.wallet)
	require.NoError(t, err)
	require.EqualValues(t, 11, got)
	for _, key := range []struct {
		chain  int64
		wallet common.Address
	}{{v.chainID + 1, rpc.wallet}, {v.chainID, common.HexToAddress("0x2222")}} {
		got, err := NextSessionEntityID(v.n.db, key.chain, owner, key.wallet)
		require.NoError(t, err)
		require.EqualValues(t, 1, got)
	}
}

func TestRetiredSessionEntityReservationFailsClosedOnCorruptOrExhaustedFloor(t *testing.T) {
	v, rpc, owner := retiredBudgetFixture(t)
	key := retiredSessionEntityKey(v.chainID, rpc.wallet)
	require.NoError(t, v.n.db.Set(key, []byte{1}))
	_, err := NextSessionEntityID(v.n.db, v.chainID, owner, rpc.wallet)
	require.ErrorContains(t, err, "invalid retired session entity reservation")
	require.NoError(t, v.n.db.Set(key, []byte{0, 0, 0, 0}))
	require.NoError(t, v.reserveRetiredSessionEntity(context.Background(), rpc.wallet, math.MaxUint32))
	_, err = NextSessionEntityID(v.n.db, v.chainID, owner, rpc.wallet)
	require.ErrorContains(t, err, SessionPolicyEntityOccupiedCode)
}

type retiredEntityWriteFailureStore struct{ storage.Storage }

func (s *retiredEntityWriteFailureStore) Set(key, value []byte) error {
	if bytes.HasPrefix(key, []byte("retiredsessionentity:")) {
		return errors.New("offline reservation storage failure")
	}
	return s.Storage.Set(key, value)
}

func TestRetiredSessionEntityStorageFailureReturnsNoCleanup(t *testing.T) {
	v, rpc, owner := retiredBudgetFixture(t)
	v.n.db = &retiredEntityWriteFailureStore{Storage: v.n.db}
	err := v.CheckAuthorities(context.Background(), owner, rpc.wallet)
	require.ErrorIs(t, err, ErrBudgetReadUnavailable)
	var refusal *BudgetPolicyError
	require.False(t, errors.As(err, &refusal), "never return a removal until entity reuse is prevented")
}
