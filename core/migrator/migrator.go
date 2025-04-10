package migrator

import (
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/AvaProtocol/EigenLayer-AVS/core/backup"
)

import (
	"fmt"
	"log"
	"sync"
)

// MigrationFunc is a function that performs a database migration. The migration functions need to follow this signature
// and return the number of records updated and an error if the migration fails
type MigrationFunc func(db storage.Storage) (int, error)

// Migration represents a database migration function
type Migration struct {
	Name     string
	Function MigrationFunc
}

// Migrator handles database migrations
type Migrator struct {
	db         storage.Storage
	migrations []Migration
	backup     *backup.Service
	mu         sync.Mutex
}

// NewMigrator creates a new migrator instance
func NewMigrator(db storage.Storage, backup *backup.Service, migrations []Migration) *Migrator {
	return &Migrator{
		db:         db,
		migrations: migrations,
		backup:     backup,
	}
}



// Register adds a new migration to the list
func (m *Migrator) Register(name string, fn MigrationFunc) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.migrations = append(m.migrations, Migration{
		Name:     name,
		Function: fn,
	})
}

// Run executes all registered migrations that haven't been run yet
func (m *Migrator) Run() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	// Check if we have any migrations to run
	hasPendingMigrations := false
	for _, migration := range m.migrations {
		key := fmt.Sprintf("migration:%s", migration.Name)
		exists, err := m.db.Exist([]byte(key))
		if err != nil || !exists {
			hasPendingMigrations = true
			break
		}
	}
	
	// If we have migrations to run, take a backup first
	if hasPendingMigrations {
		log.Printf("Pending migrations found, creating database backup before proceeding")
		if backupFile, err := m.backup.PerformBackup(); err != nil {
			return fmt.Errorf("failed to create backup before migrations: %w", err)
		} else {
			log.Printf("Database backup created at %s", backupFile)
		}
	}
	

	for _, migration := range m.migrations {
		// Check if migration has already been run
		key := fmt.Sprintf("migration:%s", migration.Name)
		exists, err := m.db.Exist([]byte(key))
		if exists && err == nil {
			log.Printf("Migration %s already applied, skipping", migration.Name)
			continue
		}
		
		// Run the migration
		log.Printf("Running migration: %s", migration.Name)
		recordsUpdated, err := migration.Function(m.db)
		if err != nil {
			return fmt.Errorf("migration %s failed: %w", migration.Name, err)
		} else {
			log.Printf("Migration %s completed successfully. %d records updated.", migration.Name, recordsUpdated)
		}
		
		// Mark migration as complete in the database
		if err := m.db.Set([]byte(key), []byte(fmt.Sprintf("records=%d,ts=%d", recordsUpdated, time.Now().UnixMilli()))); err != nil {
			return fmt.Errorf("failed to mark migration as complete in database: %w", err)
		}
	}
	
	return nil
}
