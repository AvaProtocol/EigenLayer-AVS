package backup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	backupRetention   = 3
	archiveName       = "full-backup.db.gz"
	archiveTimeLayout = "20060102T150405.000000000Z"
)

var (
	ErrBudgetExceeded    = errors.New("backup byte budget exceeded")
	ErrInsufficientSpace = errors.New("backup filesystem reserve would be exhausted")
)

// Options bounds application backups on the database volume. Zero selects
// defaults; negative values are rejected. MaxBytes includes the in-flight file.
type Options struct {
	RetentionCount int
	MaxBytes       int64
	MinFreeBytes   int64
}

func (o Options) withDefaults() Options {
	if o.RetentionCount == 0 {
		o.RetentionCount = backupRetention
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = 12 << 30
	}
	if o.MinFreeBytes == 0 {
		o.MinFreeBytes = 2 << 30
	}
	return o
}

func (o Options) Validate() error {
	if o.RetentionCount < 0 || o.MaxBytes < 0 || o.MinFreeBytes < 0 {
		return fmt.Errorf("backup retention count, byte budget and free-space reserve must not be negative")
	}
	return nil
}

type retainedBackup struct {
	path    string
	created time.Time
	bytes   int64
}

func backupTime(name string) (time.Time, error) {
	if t, err := time.Parse("06-01-02-15-04", name); err == nil {
		return t, nil
	}
	stamp, suffix, ok := strings.Cut(name, "-")
	if !ok || suffix == "" {
		return time.Time{}, fmt.Errorf("not a backup name")
	}
	for _, c := range suffix {
		if c < '0' || c > '9' {
			return time.Time{}, fmt.Errorf("not a backup suffix")
		}
	}
	return time.Parse(archiveTimeLayout, stamp)
}

// Only managed, non-empty regular backup files make a directory eligible for
// retention. Partial/empty/unknown directories and symlinks are never pruned.
func (s *Service) listBackups() ([]retainedBackup, error) {
	entries, err := os.ReadDir(s.backupDir)
	if err != nil {
		return nil, fmt.Errorf("list backups: %w", err)
	}
	var backups []retainedBackup
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		created, err := backupTime(entry.Name())
		if err != nil {
			continue
		}
		path := filepath.Join(s.backupDir, entry.Name())
		files, err := os.ReadDir(path)
		if err != nil {
			return nil, fmt.Errorf("inspect backup %s: %w", path, err)
		}
		var size int64
		managed := true
		for _, file := range files {
			info, err := file.Info()
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() {
				managed = false
				break
			}
			switch file.Name() {
			case "full-backup.db", archiveName:
				if info.Size() == 0 {
					managed = false
				}
				size += info.Size()
			case "README.txt", "restore-verification.json", "full-backup.db.gz.sha256", "full-backup.db.sha256":
				// Maintenance metadata accompanying retained legacy backups.
				size += info.Size()
			default:
				managed = false
			}
		}
		// Metadata by itself must never count as a completed backup.
		hasBackup := false
		for _, file := range files {
			if file.Name() == "full-backup.db" || file.Name() == archiveName {
				hasBackup = true
			}
		}
		if managed && hasBackup && size > 0 {
			backups = append(backups, retainedBackup{path, created, size})
		}
	}
	sort.Slice(backups, func(i, j int) bool {
		if backups[i].created.Equal(backups[j].created) {
			return backups[i].path < backups[j].path
		}
		return backups[i].created.Before(backups[j].created)
	})
	return backups, nil
}

func retainedBytes(backups []retainedBackup) int64 {
	var total int64
	for _, backup := range backups {
		total += backup.bytes
	}
	return total
}

func (s *Service) removeOldest(backups *[]retainedBackup) error {
	if len(*backups) <= 1 {
		return fmt.Errorf("refusing to remove the last completed backup")
	}
	oldest := (*backups)[0]
	if err := os.RemoveAll(oldest.path); err != nil {
		return fmt.Errorf("prune backup %s: %w", oldest.path, err)
	}
	*backups = (*backups)[1:]
	s.logger.Info("backup retention pruned old backup", "path", oldest.path)
	return nil
}

func (s *Service) pruneOldBackups(protected ...string) {
	backups, err := s.listBackups()
	if err == nil {
		// Always keep the newly published copy, including after clock rollback.
		if len(protected) > 0 {
			for i, backup := range backups {
				if backup.path == protected[0] {
					backups = append(backups[:i], backups[i+1:]...)
					backups = append(backups, backup)
					break
				}
			}
		}
		for len(backups) > 1 && (len(backups) > s.options.RetentionCount || retainedBytes(backups) > s.options.MaxBytes) {
			if err = s.removeOldest(&backups); err != nil {
				break
			}
		}
	}
	if err != nil {
		s.logger.Warn("backup retention failed", "error", err)
	}
}

// Every compressed write is checked, including the gzip footer. Older copies
// may be pruned to make room, but the newest pre-existing backup is protected
// until a replacement has been synced and published successfully.
type boundedWriter struct {
	service     *Service
	destination io.Writer
	retained    []retainedBackup
	written     int64
	failure     error
}

func (w *boundedWriter) makeRoom(next int64) error {
	for {
		free, err := w.service.freeBytes(w.service.backupDir)
		if err != nil {
			return fmt.Errorf("check backup free space: %w", err)
		}
		budgetOK := retainedBytes(w.retained) <= w.service.options.MaxBytes-w.written-next
		spaceOK := next <= free && w.service.options.MinFreeBytes <= free-next
		if budgetOK && spaceOK {
			return nil
		}
		if len(w.retained) > 1 {
			if err := w.service.removeOldest(&w.retained); err != nil {
				return err
			}
			continue
		}
		if !budgetOK {
			return fmt.Errorf("%w: limit=%d retained=%d in_progress=%d next=%d; increase backup_retention_max_bytes or compact an existing backup", ErrBudgetExceeded, w.service.options.MaxBytes, retainedBytes(w.retained), w.written, next)
		}
		return fmt.Errorf("%w: available=%d reserve=%d next=%d", ErrInsufficientSpace, free, w.service.options.MinFreeBytes, next)
	}
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if err := w.makeRoom(int64(len(p))); err != nil {
		w.failure = err
		return 0, err
	}
	n, err := w.destination.Write(p)
	w.written += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil {
		w.failure = err
	}
	return n, err
}
