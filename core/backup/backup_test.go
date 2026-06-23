package backup

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
)

func TestBackup(t *testing.T) {
	// Test cases for backup service
	t.Run("StartPeriodicBackup", func(t *testing.T) {
		// Setup
		logger := testutil.GetLogger()
		db := testutil.TestMustDB()
		tempDir := t.TempDir()

		service := NewService(logger, db, tempDir)

		// Test starting backup service
		err := service.StartPeriodicBackup(1 * time.Hour)
		if err != nil {
			t.Fatalf("Failed to start periodic backup: %v", err)
		}

		if !service.backupEnabled {
			t.Error("Backup service should be enabled after starting")
		}

		// Test starting again should fail
		err = service.StartPeriodicBackup(1 * time.Hour)
		if err == nil {
			t.Error("Starting backup service twice should return an error")
		}

		// Cleanup
		service.StopPeriodicBackup()
	})

	t.Run("StopPeriodicBackup", func(t *testing.T) {
		// Setup
		logger := testutil.GetLogger()
		db := testutil.TestMustDB()
		tempDir := t.TempDir()

		service := NewService(logger, db, tempDir)

		// Start and then stop
		_ = service.StartPeriodicBackup(1 * time.Hour)
		service.StopPeriodicBackup()

		if service.backupEnabled {
			t.Error("Backup service should be disabled after stopping")
		}

		// Test stopping when not running (should be a no-op)
		service.StopPeriodicBackup()
	})

	t.Run("PerformBackup", func(t *testing.T) {
		// Setup
		logger := testutil.GetLogger()
		db := testutil.TestMustDB()
		tempDir := t.TempDir()

		service := NewService(logger, db, tempDir)

		// Test performing a backup
		backupFile, err := service.PerformBackup()
		if err != nil {
			t.Fatalf("Failed to perform backup: %v", err)
		}

		// Verify backup file exists
		if _, err := os.Stat(backupFile); os.IsNotExist(err) {
			t.Errorf("Backup file %s does not exist", backupFile)
		}
	})
}

func TestPruneOldBackups(t *testing.T) {
	logger := testutil.GetLogger()
	tempDir := t.TempDir()
	service := NewService(logger, nil, tempDir) // pruneOldBackups never touches the db

	// backupRetention+2 timestamped backup dirs, oldest → newest.
	stamps := []string{
		"24-01-01-00-01", "24-01-01-00-02", "24-01-01-00-03",
		"24-01-01-00-04", "24-01-01-00-05",
	}
	for _, s := range stamps {
		dir := filepath.Join(tempDir, s)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "full-backup.db"), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// Non-backup entries that must survive pruning.
	if err := os.MkdirAll(filepath.Join(tempDir, "not-a-backup"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "README"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	service.pruneOldBackups()

	// Only the newest backupRetention timestamped dirs remain.
	for i, s := range stamps {
		_, err := os.Stat(filepath.Join(tempDir, s))
		shouldExist := i >= len(stamps)-backupRetention
		if shouldExist && err != nil {
			t.Errorf("expected %s to be kept, but it's gone", s)
		}
		if !shouldExist && err == nil {
			t.Errorf("expected %s to be pruned, but it remains", s)
		}
	}
	// Non-backup entries untouched.
	if _, err := os.Stat(filepath.Join(tempDir, "not-a-backup")); err != nil {
		t.Error("non-backup dir must not be pruned")
	}
	if _, err := os.Stat(filepath.Join(tempDir, "README")); err != nil {
		t.Error("stray file must not be pruned")
	}
}

// Mock implementations for testing
