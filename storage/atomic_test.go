package storage

import (
	"testing"
	"time"

	badger "github.com/dgraph-io/badger/v4"
	"github.com/stretchr/testify/require"
)

func TestAtomicAuthorityWriteRollsBackOnAnyError(t *testing.T) {
	db, err := NewWithPath(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.Set([]byte("policy"), []byte("old")))
	require.NoError(t, db.Set([]byte("hold"), []byte("paused")))
	w := db.(interface {
		AtomicWrite(map[string][]byte, [][]byte) error
	})
	err = w.AtomicWrite(map[string][]byte{"policy": []byte("active"), "": []byte("invalid-key")}, [][]byte{[]byte("hold")})
	require.Error(t, err)
	policy, err := db.GetKey([]byte("policy"))
	require.NoError(t, err)
	require.Equal(t, "old", string(policy))
	hold, err := db.GetKey([]byte("hold"))
	require.NoError(t, err)
	require.Equal(t, "paused", string(hold))
	require.NoError(t, w.AtomicWrite(map[string][]byte{"policy": []byte("active")}, [][]byte{[]byte("hold")}))
	policy, err = db.GetKey([]byte("policy"))
	require.NoError(t, err)
	require.Equal(t, "active", string(policy))
	_, err = db.GetKey([]byte("hold"))
	require.Error(t, err)
}

func TestAtomicAuthorityWriteRemovesPreparationExpiry(t *testing.T) {
	db, err := NewWithPath(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	require.NoError(t, db.SetWithTTL([]byte("preparation"), []byte("unsigned"), time.Minute))
	w := db.(*BadgerStorage)
	require.NoError(t, w.AtomicWrite(map[string][]byte{"preparation": []byte("submitting"), "hold": []byte("paused")}, nil))
	require.NoError(t, w.db.View(func(tx *badger.Txn) error {
		item, getErr := tx.Get([]byte("preparation"))
		if getErr != nil {
			return getErr
		}
		require.Zero(t, item.ExpiresAt(), "an unsettled signed operation must outlive the ten-minute window")
		return nil
	}))
}
