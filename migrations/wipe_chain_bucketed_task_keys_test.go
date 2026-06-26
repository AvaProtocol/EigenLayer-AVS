package migrations

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWipeChainBucketedTaskKeys(t *testing.T) {
	db, err := storage.NewWithPath(t.TempDir())
	require.NoError(t, err)
	defer storage.Destroy(db.(*storage.BadgerStorage))

	// OLD chain-bucketed rows (chain segment after the prefix) — must be deleted.
	old := [][]byte{
		[]byte("t:11155111:a:01abc"),
		[]byte("t:1:i:01def"),
		[]byte("u:8453:0xowner:0xwallet:01abc"),
		[]byte("history:1:01abc:01exec"),
	}
	// NEW chain-agnostic rows — must survive.
	keep := [][]byte{
		[]byte("t:a:01abc"),
		[]byte("t:i:01def"),
		[]byte("u:0xowner:0xwallet:01abc"),
		[]byte("history:01abc:01exec"),
	}
	// Unrelated namespaces — untouched.
	other := [][]byte{
		[]byte("w:1:0xowner:0xwallet"), // wallet keys stay per-chain
		[]byte("secret:0xowner:foo"),
	}

	for _, k := range append(append(append([][]byte{}, old...), keep...), other...) {
		require.NoError(t, db.Set(k, []byte("1")))
	}

	n, err := WipeChainBucketedTaskKeys(db)
	require.NoError(t, err)
	assert.Equal(t, len(old), n, "should delete exactly the old chain-bucketed rows")

	for _, k := range old {
		exists, _ := db.Exist(k)
		assert.False(t, exists, "old key must be deleted: %s", k)
	}
	for _, k := range append(append([][]byte{}, keep...), other...) {
		exists, _ := db.Exist(k)
		assert.True(t, exists, "key must survive: %s", k)
	}

	// Idempotent: a second run finds nothing.
	n2, err := WipeChainBucketedTaskKeys(db)
	require.NoError(t, err)
	assert.Equal(t, 0, n2)
}
