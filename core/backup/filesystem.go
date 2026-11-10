package backup

import (
	"math"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func availableBytes(path string) (int64, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Bsize <= 0 || int64(stat.Bavail) < 0 {
		return 0, nil
	}
	blocks, blockSize := uint64(stat.Bavail), uint64(stat.Bsize)
	if blockSize != 0 && blocks > math.MaxInt64/blockSize {
		return math.MaxInt64, nil
	}
	return int64(blocks * blockSize), nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// Persist newly created ancestor entries as well as the archive directory.
// Syncing only the leaf cannot guarantee that a new backup root survives a crash.
func (s *Service) ensureBackupDirectory() error {
	var missing []string
	for path := filepath.Clean(s.backupDir); ; path = filepath.Dir(path) {
		if _, err := os.Stat(path); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return err
		}
		missing = append(missing, path)
	}
	if err := os.MkdirAll(s.backupDir, 0755); err != nil {
		return err
	}
	for _, path := range missing {
		if err := s.syncDir(filepath.Dir(path)); err != nil {
			return err
		}
	}
	return nil
}
