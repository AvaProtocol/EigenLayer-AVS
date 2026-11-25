package storage

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Values of 300 KB sit inline in the tables (under Badger's 1 MB default
// ValueThreshold), like the gateway's execution records. Flatten must keep
// live keys readable, leave deleted ones gone, and leave the DB writable.
func TestFlattenKeepsLiveKeysAndDropsDeleted(t *testing.T) {
	db, err := NewWithPath(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	value := bytes.Repeat([]byte("x"), 300<<10)
	key := func(i int) []byte { return []byte(fmt.Sprintf("history:task:%02d", i)) }
	for i := 0; i < 20; i++ {
		require.NoError(t, db.Set(key(i), value))
	}
	for i := 0; i < 15; i++ {
		require.NoError(t, db.Delete(key(i)))
	}

	require.NoError(t, db.Flatten(1))

	for i := 0; i < 20; i++ {
		exists, err := db.Exist(key(i))
		require.NoError(t, err)
		require.Equal(t, i >= 15, exists, "key %d", i)
	}
	require.NoError(t, db.Set([]byte("after"), []byte("ok")))
}
