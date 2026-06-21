package migrations

import (
	"github.com/AvaProtocol/EigenLayer-AVS/core/migrator"
)

// Migrations contains the list of database migrations to be applied
// Completed migrations have been moved to docs/historical-migrations/2025-completed/
// to reduce startup overhead and avoid unnecessary processing.
var Migrations = []migrator.Migration{
	// {
	// 	// The name of the migration will be recorded in our key-value store, and it's sorted lexicographically
	// 	// so we can use the timestamp to sort the migrations in the right order for debugging
	// 	// We should prefix the name with the timestamp in format of YYYYMMDD-HHMMSS
	// 	// Not a requirement but strongly recommended
	// 	Name:     "20250405-232000-change-epoch-to-ms",
	// 	Function: ChangeEpochToMs,
	// },

	// ========================================
	// COMPLETED MIGRATIONS (moved to historical)
	// ========================================
	// - 20250603-183034-token-metadata-fields (moved to docs/historical-migrations/2025-completed/)
	// - 20250128-120000-protobuf-structure-cleanup (moved to docs/historical-migrations/2025-completed/)
	// - 20250913-185000-add-execution-indexes (moved to docs/historical-migrations/2025-completed/)
	// - 20260618-delete-invalid-failed-tasks (moved to docs/historical-migrations/2026-completed/ — applied in v3.10.3, 33 records)
	// - 20260621-delete-auto-disabled-invalid-tasks (moved to docs/historical-migrations/2026-completed/ — applied in v3.10.3, 12 records: EIGENLAYER-AVS-1X..28)
	//
	// These migrations have been successfully applied in production. They have
	// been archived (behind the historical_migrations build tag) to reduce
	// startup overhead. Migration completion records remain in the database
	// (migration:* keys), so even if re-registered they would be skipped.

	// ========================================
	// ACTIVE MIGRATIONS
	// ========================================
	// Add new migrations here that need to be applied
}
