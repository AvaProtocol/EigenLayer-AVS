package migrations

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// WipeChainBucketedTaskKeys deletes task-related rows written under the old
// chain-bucketed key schema, retired by G5 (see PLAN_CHAIN_DECOUPLING.md).
//
// Before G5, a task carried a chain_id and storage keyed every row by it:
//
//	t:<chainID>:<status>:<id>            (workflow)
//	u:<chainID>:<owner>:<wallet>:<id>    (user index)
//	history:<chainID>:<taskID>:<execID>  (execution)
//
// G5 removed the task-level chain — a task belongs to no chain; each
// chain-aware trigger/node carries its own. Storage is now chain-agnostic:
//
//	t:<status>:<id>            u:<owner>:<wallet>:<id>            history:<taskID>:<execID>
//
// The new readers never look under the old keys, so any pre-G5 row is dead.
// We do NOT backfill them onto the new schema — re-deriving a chain on a task's
// behalf is guesswork, and the deployed set is tiny. Owners re-create affected
// workflows with explicit per-part chains. This migration removes the orphans.
//
// Detection is unambiguous: the segment immediately after the prefix is a
// chain_id (a plain integer) in the OLD schema, but a status letter (t:),
// a 0x-prefixed address (u:), or a ULID (history:) in the NEW schema — none of
// which parse as an int64. So "first segment parses as int64" == old key.
//
// Safety: the migrator takes a full DB backup before running and records
// completion so this runs exactly once; re-running finds nothing.
func WipeChainBucketedTaskKeys(db storage.Storage) (int, error) {
	var toDelete [][]byte
	for _, prefix := range []string{"t:", "u:", "history:"} {
		err := db.IterateKeysOnly([]byte(prefix), func(key []byte) error {
			parts := strings.SplitN(string(key), ":", 3)
			if len(parts) < 3 {
				return nil
			}
			// parts[1] is the first segment after the prefix. Only the old
			// chain-bucketed schema has a plain integer here.
			if _, err := strconv.ParseInt(parts[1], 10, 64); err != nil {
				return nil
			}
			// Key bytes are iterator-owned — copy before retaining.
			toDelete = append(toDelete, append([]byte{}, key...))
			return nil
		})
		if err != nil {
			return 0, fmt.Errorf("scan %q: %w", prefix, err)
		}
	}

	deleted := 0
	for _, key := range toDelete {
		if err := db.Delete(key); err != nil {
			return deleted, fmt.Errorf("delete %q: %w", string(key), err)
		}
		deleted++
	}
	return deleted, nil
}
