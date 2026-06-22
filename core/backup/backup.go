package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/Layr-Labs/eigensdk-go/logging"
)

type Service struct {
	logger        logging.Logger
	db            storage.Storage
	backupDir     string
	backupEnabled bool
	interval      time.Duration
	stop          chan struct{}
}

func NewService(logger logging.Logger, db storage.Storage, backupDir string) *Service {
	return &Service{
		logger:        logger,
		db:            db,
		backupDir:     backupDir,
		backupEnabled: false,
		stop:          make(chan struct{}),
	}
}

func (s *Service) StartPeriodicBackup(interval time.Duration) error {
	if s.backupEnabled {
		return fmt.Errorf("backup service already running")
	}

	if err := os.MkdirAll(s.backupDir, 0755); err != nil {
		return fmt.Errorf("failed to create backup directory: %v", err)
	}

	s.interval = interval
	s.backupEnabled = true

	go s.backupLoop()

	s.logger.Infof("Started periodic backup every %v to %s", interval, s.backupDir)
	return nil
}

func (s *Service) StopPeriodicBackup() {
	if !s.backupEnabled {
		return
	}

	s.backupEnabled = false
	close(s.stop)
	s.logger.Infof("Stopped periodic backup")
}

func (s *Service) backupLoop() {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if backupFile, err := s.PerformBackup(); err != nil {
				s.logger.Errorf("Periodic backup failed: %v", err)
			} else {
				s.logger.Infof("Periodic backup completed successfully to %s", backupFile)
			}
		case <-s.stop:
			return
		}
	}
}

func (s *Service) PerformBackup() (string, error) {
	timestamp := time.Now().Format("06-01-02-15-04")
	backupPath := filepath.Join(s.backupDir, timestamp)

	if err := os.MkdirAll(backupPath, 0755); err != nil {
		return "", fmt.Errorf("failed to create backup timestamp directory: %v", err)
	}

	backupFile := filepath.Join(backupPath, "full-backup.db")
	f, err := os.Create(backupFile)
	if err != nil {
		return "", fmt.Errorf("failed to create backup file: %v", err)
	}
	defer f.Close()

	s.logger.Infof("Running backup to %s", backupFile)
	since := uint64(0) // Full backup
	_, err = s.db.Backup(context.Background(), f, since)
	if err != nil {
		return "", fmt.Errorf("backup operation failed: %v", err)
	}

	s.logger.Infof("Backup completed successfully to %s", backupFile)
	s.pruneOldBackups()
	return backupFile, nil
}

// backupRetention is the number of most-recent full-backup directories kept
// under backupDir; older ones are pruned after each successful backup. These
// backups are a pre-migration / periodic safety net living on the SAME volume
// as the DB, so they're redundant with Railway's own volume snapshots — keeping
// a few is plenty. Not pruning them silently bloats the volume: one
// ~0.5–0.7 GB full-backup.db is written per migration-bearing release.
const backupRetention = 3

// pruneOldBackups removes all but the most recent backupRetention timestamped
// backup directories under backupDir. Names use a lexicographically-sortable
// timestamp (06-01-02-15-04), so a name sort is a chronological sort. Failures
// are logged, never returned — a prune problem must not fail the backup (or the
// migration that triggered it).
func (s *Service) pruneOldBackups() {
	entries, err := os.ReadDir(s.backupDir)
	if err != nil {
		s.logger.Warnf("backup retention: cannot read %s: %v", s.backupDir, err)
		return
	}

	var stamps []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Only real backup dirs — names that parse as the backup timestamp.
		if _, perr := time.Parse("06-01-02-15-04", e.Name()); perr != nil {
			continue
		}
		stamps = append(stamps, e.Name())
	}

	if len(stamps) <= backupRetention {
		return
	}

	sort.Strings(stamps) // oldest first
	for _, name := range stamps[:len(stamps)-backupRetention] {
		path := filepath.Join(s.backupDir, name)
		if rmErr := os.RemoveAll(path); rmErr != nil {
			s.logger.Errorf("backup retention: failed to prune %s: %v", path, rmErr)
		} else {
			s.logger.Infof("backup retention: pruned old backup %s", path)
		}
	}
}
