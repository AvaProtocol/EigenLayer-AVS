//go:build linux || darwin

package backup

import (
	"errors"
	"os"
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

func TestAncestorSyncRetriedAfterFailureAndRestart(t *testing.T) {
	for _, restart := range []bool{false, true} {
		name := "same service"
		if restart {
			name = "restarted service"
		}
		t.Run(name, func(t *testing.T) {
			s := testService(t, &backupSource{payload: []byte("retry backup")}, Options{})
			base := s.backupDir
			s.backupDir = filepath.Join(base, "outer", "inner")
			failure := errors.New("ancestor sync failed")
			attempts := 0
			for attempt := 0; attempt < 2; attempt++ {
				if attempt == 1 && restart {
					previous := s
					s = NewService(previous.logger, previous.db, previous.backupDir, previous.options)
					s.freeBytes = previous.freeBytes
				}
				s.syncDir = func(path string) error {
					if path == base {
						attempts++
						if attempts == 1 {
							return failure
						}
					}
					return syncDirectory(path)
				}
				path, err := s.PerformBackup()
				if attempt == 0 {
					require.ErrorIs(t, err, failure)
					_, err = os.Stat(s.backupDir)
					require.NoError(t, err, "MkdirAll leaves directories after sync failure")
				} else {
					require.NoError(t, err)
					_, err = os.Stat(path)
					require.NoError(t, err)
				}
			}
			require.Equal(t, 2, attempts, "retry must sync the previously failed ancestor")
		})
	}
}
