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

// Persist the ancestor chain on every attempt, including directories left by a
// failed sync in this process or before restart. Stop at the filesystem boundary:
// ancestors beyond the existing mount cannot have been created by MkdirAll.
func (s *Service) ensureBackupDirectory() error {
	if err := os.MkdirAll(s.backupDir, 0755); err != nil {
		return err
	}
	root, err := filepath.Abs(s.backupDir)
	if err != nil {
		return err
	}
	var rootStat unix.Stat_t
	if err := unix.Stat(root, &rootStat); err != nil {
		return err
	}
	for path := filepath.Dir(root); ; path = filepath.Dir(path) {
		var stat unix.Stat_t
		if err := unix.Stat(path, &stat); err != nil {
			return err
		}
		if stat.Dev != rootStat.Dev {
			break
		}
		if err := s.syncDir(path); err != nil {
			return err
		}
		if filepath.Dir(path) == path {
			break
		}
	}
	return nil
}
