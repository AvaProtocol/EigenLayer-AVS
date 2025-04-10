package migrations

import (
	"github.com/AvaProtocol/EigenLayer-AVS/core/migrator"
)

var Migrations = []migrator.Migration{
	{
		// The name of the migration will be recored in our key-value store, and it's sorted lexicographically
		// so we can use the timestamp to sort the migrations in the right order for debugging
		// We should prefix the name with the timestamp in format of YYYYMMDD-HHMMSS
		// Not a requirement but strongly recommended
		Name:     "20250405-232000-change-epoch-to-ms",
		Function: ChangeEpochToMs,
	},
	// Each migration should be added to this list
}
