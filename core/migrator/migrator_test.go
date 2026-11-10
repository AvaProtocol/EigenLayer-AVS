package migrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/backup"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/require"
)

func TestMigrator(t *testing.T) {
	// Setup test database
	logger := testutil.GetLogger()
	db := testutil.TestMustDB()
	defer db.Close()

	// Create backup service
	backupDir := t.TempDir()
	backup := backup.NewService(logger, db, backupDir)

	// Test migration function that updates records
	testMigration := func(db storage.Storage) (int, error) {
		return 5, db.Set([]byte("test:key"), []byte("migrated"))
	}

	// Create migrator with test migration
	migrations := []Migration{} // Initialize with empty slice
	migrator := NewMigrator(db, backup, migrations)
	migrator.Register("test_migration", testMigration)

	// Run migrations
	err := migrator.Run()
	if err != nil {
		t.Fatalf("Failed to run migrations: %v", err)
	}

	// Verify migration was marked as complete
	migrationKey := []byte("migration:test_migration")
	exists, err := db.Exist(migrationKey)
	if err != nil {
		t.Fatalf("Failed to check if migration exists: %v", err)
	}
	if !exists {
		t.Fatalf("Migration was not marked as complete")
	}

	// Verify migration record format (should contain records count and timestamp)
	migrationData, err := db.GetKey(migrationKey)
	if err != nil {
		t.Fatalf("Failed to get migration data: %v", err)
	}

	migrationRecord := string(migrationData)
	if !strings.Contains(migrationRecord, "records=5") {
		t.Errorf("Migration record doesn't contain correct record count: %s", migrationRecord)
	}
	if !strings.Contains(migrationRecord, "ts=") {
		t.Errorf("Migration record doesn't contain timestamp: %s", migrationRecord)
	}

	// Test that migrations aren't run twice
	// Create a counter to track if migration is called
	migrationCounter := 0
	countingMigration := func(db storage.Storage) (int, error) {
		migrationCounter++
		return 0, nil
	}

	// Register a new migration that we've already run
	migrator.Register("test_migration", countingMigration)

	// Run migrations again
	err = migrator.Run()
	if err != nil {
		t.Fatalf("Failed to run migrations second time: %v", err)
	}

	// Verify the migration wasn't executed again
	if migrationCounter > 0 {
		t.Errorf("Migration was executed again when it should have been skipped")
	}

	// Test new migration gets executed
	migrator.Register("second_migration", countingMigration)

	// Run migrations again
	err = migrator.Run()
	if err != nil {
		t.Fatalf("Failed to run migrations third time: %v", err)
	}

	// Verify the new migration was executed
	if migrationCounter != 1 {
		t.Errorf("New migration was not executed")
	}

	// Verify second migration was marked as complete
	secondMigrationKey := []byte("migration:second_migration")
	exists, err = db.Exist(secondMigrationKey)
	if err != nil {
		t.Fatalf("Failed to check if second migration exists: %v", err)
	}
	if !exists {
		t.Fatalf("Second migration was not marked as complete")
	}
}

func TestMigrationDoesNotRunWhenBackupCannotFit(t *testing.T) {
	db, err := storage.NewWithPath(t.TempDir())
	require.NoError(t, err)
	defer db.Close()
	root := t.TempDir()
	previous := filepath.Join(root, "26-01-01-00-00")
	require.NoError(t, os.Mkdir(previous, 0700))
	previous = filepath.Join(previous, "full-backup.db")
	require.NoError(t, os.WriteFile(previous, []byte("last backup"), 0600))
	svc := backup.NewService(testutil.GetLogger(), db, root, backup.Options{MaxBytes: 1, MinFreeBytes: 1})
	called := false
	m := NewMigrator(db, svc, []Migration{{Name: "must-not-run", Function: func(storage.Storage) (int, error) { called = true; return 1, nil }}})
	err = m.Run()
	require.ErrorIs(t, err, backup.ErrBudgetExceeded)
	require.False(t, called)
	exists, err := db.Exist([]byte("migration:must-not-run"))
	require.NoError(t, err)
	require.False(t, exists)
	data, err := os.ReadFile(previous)
	require.NoError(t, err)
	require.Equal(t, "last backup", string(data))
}
