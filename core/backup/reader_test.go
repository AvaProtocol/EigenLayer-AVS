package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/dgraph-io/badger/v4/pb"
	"github.com/stretchr/testify/require"
)

func TestRawRestoreWithGzipPrefixCollision(t *testing.T) {
	for _, tc := range []struct {
		name      string
		batchSize int
		filename  string
	}{
		{"legacy filename", 35615, "full-backup.db"},
		{"automatic header fallback", 35615, "renamed-backup"},
		{"complete gzip header collision", 559903, "full-backup.db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			key := []byte("legacy:collision")
			kv := &pb.KV{Key: key, Version: 1}
			batch := &pb.KVList{Kv: []*pb.KV{kv}}
			var encoded []byte
			valueSize := tc.batchSize - 64
			for i := 0; i < 5; i++ {
				kv.Value = bytes.Repeat([]byte("x"), valueSize)
				var err error
				encoded, err = batch.Marshal()
				require.NoError(t, err)
				if len(encoded) == tc.batchSize {
					break
				}
				valueSize += tc.batchSize - len(encoded)
			}
			require.Len(t, encoded, tc.batchSize)
			raw := make([]byte, 8+len(encoded))
			binary.LittleEndian.PutUint64(raw[:8], uint64(len(encoded)))
			copy(raw[8:], encoded)
			require.Equal(t, []byte{0x1f, 0x8b}, raw[:2])
			if tc.batchSize == 559903 {
				gz, err := gzip.NewReader(bytes.NewReader(raw))
				require.NoError(t, err, "length can even mimic a complete gzip header")
				require.NoError(t, gz.Close())
			}
			path := filepath.Join(t.TempDir(), tc.filename)
			require.NoError(t, os.WriteFile(path, raw, 0600))
			r, err := Open(path)
			require.NoError(t, err)
			defer r.Close()
			db, err := storage.NewWithPath(t.TempDir())
			require.NoError(t, err)
			defer db.Close()
			require.NoError(t, db.Load(context.Background(), r))
			_, err = io.Copy(io.Discard, r)
			require.NoError(t, err)
			got, err := db.GetKey(key)
			require.NoError(t, err)
			require.Equal(t, kv.Value, got)
			// An explicit gzip path must not fall back to raw on corruption.
			if tc.batchSize == 35615 {
				gzipPath := path + ".gz"
				require.NoError(t, os.WriteFile(gzipPath, raw, 0600))
				_, err := Open(gzipPath)
				require.Error(t, err)
			}
		})
	}
}
