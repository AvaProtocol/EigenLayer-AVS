package migrations

import (
	"github.com/AvaProtocol/EigenLayer-AVS/core/migrator"
)

// Migrations contains the list of database migrations to be applied
// We keep this as an empty array after migrations are applied to avoid complexity
// Any future migrations should be applied manually or through separate tooling
var Migrations = []migrator.Migration{
	// {
	// 	// The name of the migration will be recored in our key-value store, and it's sorted lexicographically
	// 	// so we can use the timestamp to sort the migrations in the right order for debugging
	// 	// We should prefix the name with the timestamp in format of YYYYMMDD-HHMMSS
	// 	// Not a requirement but strongly recommended
	// 	Name:     "20250405-232000-change-epoch-to-ms",
	// 	Function: ChangeEpochToMs,
	// },
	// Token metadata fields migration - adding required fields to new TokenMetadata struct
	{
		Name:     "20250603-183034-token-metadata-fields",
		Function: TokenMetadataFieldsMigration,
	},
	// Each migration should be added to this list
}
