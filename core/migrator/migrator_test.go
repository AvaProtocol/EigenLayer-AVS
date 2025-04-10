package migrator

import (
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/backup"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
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