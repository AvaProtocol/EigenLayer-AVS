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
	//
	// These migrations have been successfully applied in production and consistently
	// report 0 records updated. They have been archived to reduce startup overhead.
	// Migration completion records remain in the database (migration:* keys).

	// ========================================
	// ACTIVE MIGRATIONS
	// ========================================
	// Add new migrations here that need to be applied
	{
		// One-time cleanup of the legacy invalid-task cohort: workflow rows
		// that are Failed AND still fail config validation (invalid node
		// names / configs orphaned by an old proto migration). See
		// delete_invalid_failed_tasks.go. Idempotent: re-running finds none.
		Name:     "20260618-delete-invalid-failed-tasks",
		Function: DeleteInvalidFailedTasks,
	},
	{
		// One-time cleanup of the auto-disabled invalid-task cohort: workflow
		// rows the executor flipped to Disabled after the consecutive-permanent-
		// validation-failure threshold — overwhelmingly "smart wallet address
		// does not belong to owner" (EIGENLAYER-AVS-1X..28). They pass config
		// validation, so DeleteInvalidFailedTasks leaves them; this removes them.
		// See delete_auto_disabled_invalid_tasks.go. Idempotent: re-running
		// finds none.
		Name:     "20260621-delete-auto-disabled-invalid-tasks",
		Function: DeleteAutoDisabledInvalidTasks,
	},
}
