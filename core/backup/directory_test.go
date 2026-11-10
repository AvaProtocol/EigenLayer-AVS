package backup

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFirstBackupSyncsNewDirectoryAncestors(t *testing.T) {
	s := testService(t, &backupSource{payload: []byte("first backup")}, Options{})
	base := s.backupDir
	s.backupDir = filepath.Join(base, "outer", "inner")
	var synced []string
	s.syncDir = func(path string) error { synced = append(synced, path); return syncDirectory(path) }
	_, err := s.PerformBackup()
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(synced), 4)
	require.Equal(t, []string{filepath.Join(base, "outer"), base}, synced[:2])
	require.Equal(t, s.backupDir, synced[len(synced)-1])
}
