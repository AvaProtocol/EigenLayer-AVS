package storage

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// sstBytes is the size of the Badger tables in dir.
func sstBytes(t *testing.T, dir string) int64 {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.sst"))
	require.NoError(t, err)
	var total int64
	for _, p := range paths {
		info, err := os.Stat(p)
		require.NoError(t, err)
		total += info.Size()
	}
	return total
}

// Flatten must reclaim the table space of deleted records that sit on disk,
// as the gateway's do: values of 300 KB, under Badger's 1 MB default
// ValueThreshold, stored inline in the tables, with the tombstones from a
// later delete in a separate level-0 table.
//
// Closing flushes the memtable to a level-0 table (CompactL0OnClose is off),
// so the fixture is built across reopens. Flatten does nothing while every
// table is still on level 0 with room to spare, so the records are first
// pushed down a level, then deleted.
func TestFlattenReclaimsDeletedInlineValues(t *testing.T) {
	dir := t.TempDir()
	open := func() Storage {
		db, err := NewWithPath(dir)
		require.NoError(t, err)
		return db
	}
	key := func(i int) []byte { return []byte(fmt.Sprintf("history:task:%02d", i)) }
	values := make([][]byte, 20)
	rng := rand.New(rand.NewSource(1))
	for i := range values {
		// Random, so table compression cannot hide how much is stored.
		values[i] = make([]byte, 300<<10)
		_, _ = rng.Read(values[i])
	}

	// Five level-0 tables, the default level-0 limit, so Flatten moves them
	// down a level instead of returning early.
	for batch := 0; batch < 5; batch++ {
		db := open()
		for i := batch * 4; i < batch*4+4; i++ {
			require.NoError(t, db.Set(key(i), values[i]))
		}
		require.NoError(t, db.Close())
	}
	db := open()
	require.NoError(t, db.Flatten(1))
	for i := 0; i < 15; i++ {
		require.NoError(t, db.Delete(key(i)))
	}
	require.NoError(t, db.Close())

	before := sstBytes(t, dir)
	require.Greater(t, before, int64(5<<20), "the records should be on disk before Flatten")

	db = open()
	require.NoError(t, db.Flatten(1))
	require.NoError(t, db.Close())

	after := sstBytes(t, dir)
	// 5 of 20 records survive, so about a quarter of the space should remain.
	require.Less(t, after, before/2, "Flatten should drop the deleted records' table space (before %d, after %d)", before, after)

	db = open()
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	for i := 0; i < 20; i++ {
		got, err := db.GetKey(key(i))
		if i < 15 {
			require.Error(t, err, "deleted key %d", i)
			continue
		}
		require.NoError(t, err, "key %d", i)
		require.True(t, bytes.Equal(values[i], got), "key %d changed", i)
	}
}
