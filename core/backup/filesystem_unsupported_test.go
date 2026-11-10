//go:build !linux && !darwin

package backup

import (
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
	"github.com/stretchr/testify/require"
)

func TestUnsupportedPlatformPreservesExistingBackups(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"26-01-01-00-00", "26-01-02-00-00"} {
		dir := filepath.Join(root, name)
		require.NoError(t, os.Mkdir(dir, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "full-backup.db"), []byte(name), 0600))
	}
	// A nil DB proves rejection happens before any attempt to export data;
	// retention count one would remove an old copy if the platform guard ran late.
	s := NewService(logger.NewNoOpLogger(), nil, root, Options{RetentionCount: 1})
	_, err := s.PerformBackup()
	require.ErrorIs(t, err, errBackupFilesystemUnsupported)
	require.ErrorIs(t, s.StartPeriodicBackup(time.Hour), errBackupFilesystemUnsupported)
	require.False(t, s.backupEnabled)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(root, entry.Name(), "full-backup.db"))
		require.NoError(t, err)
		require.Equal(t, entry.Name(), string(data))
	}
}

func TestCompressedRecoveryReaderOnUnsupportedBackupPlatform(t *testing.T) {
	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, err := gz.Write([]byte("recovery remains available"))
	require.NoError(t, err)
	require.NoError(t, gz.Close())
	path := filepath.Join(t.TempDir(), "full-backup.db.gz")
	require.NoError(t, os.WriteFile(path, compressed.Bytes(), 0600))
	r, err := Open(path)
	require.NoError(t, err)
	defer r.Close()
	data, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, "recovery remains available", string(data))
}
