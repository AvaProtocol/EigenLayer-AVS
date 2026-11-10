package backup

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/Layr-Labs/eigensdk-go/logging"
)

type Service struct {
	logger        logging.Logger
	db            storage.Storage
	backupDir     string
	mu            sync.Mutex
	backupEnabled bool
	interval      time.Duration
	stop          chan struct{}
	wg            sync.WaitGroup
	workMu        sync.Mutex // serializes migration and periodic backup writes/pruning
	options       Options
	freeBytes     func(string) (int64, error)
	createFile    func(string) (archiveFile, error)
	rename        func(string, string) error
	syncDir       func(string) error
}

func NewService(logger logging.Logger, db storage.Storage, backupDir string, options ...Options) *Service {
	opts := Options{}.withDefaults()
	if len(options) > 0 {
		opts = options[0].withDefaults()
	}
	return &Service{
		logger:        logger,
		db:            db,
		backupDir:     backupDir,
		backupEnabled: false,
		options:       opts,
		freeBytes:     availableBytes,
		createFile: func(path string) (archiveFile, error) {
			return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		},
		rename:  os.Rename,
		syncDir: syncDirectory,
	}
}

func (s *Service) StartPeriodicBackup(interval time.Duration) error {
	if err := s.options.Validate(); err != nil {
		return err
	}
	if interval <= 0 {
		return fmt.Errorf("backup interval must be positive")
	}
	if s.backupDir == "" {
		return fmt.Errorf("backup directory is empty")
	}
	if err := s.ensureBackupDirectory(); err != nil {
		return fmt.Errorf("failed to create backup directory: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.backupEnabled {
		return fmt.Errorf("backup service already running")
	}

	s.interval = interval
	s.stop = make(chan struct{})
	s.backupEnabled = true
	s.wg.Add(1)
	stop := s.stop
	go func() {
		defer s.wg.Done()
		s.backupLoop(stop)
	}()

	s.logger.Infof("Started periodic backup every %v to %s", interval, s.backupDir)
	return nil
}

func (s *Service) StopPeriodicBackup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.backupEnabled {
		return
	}
	s.backupEnabled = false
	close(s.stop)
	s.stop = nil
	// backupLoop / PerformBackup never take mu, so waiting here is safe
	// and serializes Stop with a concurrent Start. aggregator shutdown
	// then db.Close() cannot race an in-flight db.Backup.
	s.wg.Wait()
	s.logger.Infof("Stopped periodic backup")
}

func (s *Service) backupLoop(stop <-chan struct{}) {
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
		case <-stop:
			return
		}
	}
}

func (s *Service) PerformBackup() (string, error) {
	s.workMu.Lock()
	defer s.workMu.Unlock()
	if err := s.options.Validate(); err != nil {
		return "", err
	}
	if strings.TrimSpace(s.backupDir) == "" {
		return "", fmt.Errorf("backup directory is empty")
	}
	if err := s.ensureBackupDirectory(); err != nil {
		return "", fmt.Errorf("create backup directory: %w", err)
	}
	retained, err := s.listBackups()
	if err != nil {
		return "", err
	}
	// Make a count slot before writing, but never delete the final existing
	// backup before its replacement is durably published (including count=1).
	count := max(1, s.options.RetentionCount-1)
	for len(retained) > count {
		if err := s.removeOldest(&retained); err != nil {
			return "", err
		}
	}
	w := &boundedWriter{service: s, retained: retained}
	if err := w.makeRoom(1); err != nil {
		return "", err
	}
	stamp := time.Now().UTC().Format(archiveTimeLayout)
	partial, err := os.MkdirTemp(s.backupDir, ".partial-"+stamp+"-")
	if err != nil {
		return "", fmt.Errorf("create partial backup: %w", err)
	}
	cleanupPath := partial
	complete := false
	defer func() {
		if !complete {
			if err := os.RemoveAll(cleanupPath); err != nil {
				s.logger.Error("backup partial cleanup failed", "path", cleanupPath, "error", err)
			}
		}
	}()
	path := filepath.Join(partial, archiveName)
	s.logger.Info("running compressed backup", "path", path,
		"retention_count", s.options.RetentionCount,
		"max_bytes", s.options.MaxBytes, "min_free_bytes", s.options.MinFreeBytes)
	if err := s.writeArchive(path, w); err != nil {
		return "", fmt.Errorf("backup operation failed: %w", err)
	}
	if err := s.syncDir(partial); err != nil {
		return "", fmt.Errorf("sync backup directory: %w", err)
	}
	published := filepath.Join(s.backupDir, strings.TrimPrefix(filepath.Base(partial), ".partial-"))
	if _, err := os.Lstat(published); !os.IsNotExist(err) {
		return "", fmt.Errorf("backup destination is not available: %s", published)
	}
	if err := s.rename(partial, published); err != nil {
		return "", fmt.Errorf("publish backup: %w", err)
	}
	cleanupPath = published
	if err := s.syncDir(s.backupDir); err != nil {
		return "", fmt.Errorf("sync backup publication: %w", err)
	}
	complete = true
	backupFile := filepath.Join(published, archiveName)
	s.logger.Info("backup completed", "path", backupFile, "compressed_bytes", w.written)
	s.pruneOldBackups(published)
	return backupFile, nil
}

type archiveFile interface {
	io.Writer
	Sync() error
	Close() error
}

func (s *Service) writeArchive(path string, w *boundedWriter) (err error) {
	f, err := s.createFile(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	w.destination = f
	// BestSpeed keeps migration/startup backups from spending minutes at the
	// default compression level. The compressed byte budget remains enforced.
	gz, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
	if err != nil {
		return err
	}
	if _, err := s.db.Backup(context.Background(), gz, 0); err != nil {
		// Badger's parallel stream may return context.Canceled after a writer
		// failure; retain the actual space/budget/I/O cause for operators.
		return errors.Join(err, w.failure)
	}
	if err := gz.Close(); err != nil {
		return fmt.Errorf("finish gzip backup: %w", err)
	}
	return f.Sync()
}
