

package backup

import (
	"os"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
)

func TestBackup(t *testing.T) {
	if os.Getenv("CI") == "true" {
		t.Skip("Skipping test in CI environment due to Go version compatibility issues")
	}
	
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

// Mock implementations for testing
