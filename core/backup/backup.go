package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	return backupFile, nil
}
