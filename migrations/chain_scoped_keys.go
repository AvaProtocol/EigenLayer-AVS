package migrations

import (
	"fmt"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/core/migrator"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	storageschema "github.com/AvaProtocol/EigenLayer-AVS/storage/schema"
)

// MigrateKeysToChainScoped rewrites legacy single-chain storage keys to the
// chain-scoped format so the gateway can host tasks for multiple chains in
// one BadgerDB without prefix collisions.
//
// Legacy → chain-scoped:
//
//	t:{status}:{taskID}                   → t:{chainID}:{status}:{taskID}
//	u:{owner}:{wallet}:{taskID}           → u:{chainID}:{owner}:{wallet}:{taskID}
//	history:{taskID}:{executionID}        → history:{chainID}:{taskID}:{executionID}
//
// The migration is idempotent: re-running on a partially-migrated DB skips
// keys that are already in chain-scoped form and only rewrites the legacy
// stragglers. Each key is moved atomically via storage.Move (single BadgerDB
// transaction), so a crash mid-rename can never leave a key half-written.
//
// chainID is supplied by the aggregator at startup — every legacy key on disk
// belongs to whichever single chain that aggregator was bound to before the
// gateway architecture landed, so stamping all of them with that value is
// correct. Once this migration has run, future writers must use the
// chain-scoped storage helpers.
func MigrateKeysToChainScoped(db storage.Storage, chainID int64) (int, error) {
	if chainID <= 0 {
		return 0, fmt.Errorf("invalid chain_id for migration: %d", chainID)
	}

	total := 0

	// 1) Task-by-status keys: t:{status}:{taskID}
	// Iterate every status bucket because we need to rewrite all of them.
	for _, status := range []avsproto.TaskStatus{
		avsproto.TaskStatus_Enabled,
		avsproto.TaskStatus_Disabled,
		avsproto.TaskStatus_Running,
		avsproto.TaskStatus_Completed,
		avsproto.TaskStatus_Failed,
	} {
		legacyPrefix := string(storageschema.WorkflowByStatusStoragePrefix(status))
		n, err := migrateTaskStatusBucket(db, chainID, legacyPrefix, status)
		if err != nil {
			return total, fmt.Errorf("task-status bucket %q: %w", legacyPrefix, err)
		}
		total += n
	}

	// 2) User-task keys: u:{owner}:{wallet}:{taskID}
	n, err := migrateUserTaskKeys(db, chainID)
	if err != nil {
		return total, fmt.Errorf("user-task keys: %w", err)
	}
	total += n

	// 3) Execution-history keys: history:{taskID}:{executionID}
	n, err = migrateExecutionHistoryKeys(db, chainID)
	if err != nil {
		return total, fmt.Errorf("execution-history keys: %w", err)
	}
	total += n

	return total, nil
}

// migrateTaskStatusBucket rewrites legacy t:{status}:{taskID} keys for one
// status bucket into chain-scoped t:{chainID}:{status}:{taskID} form. Keys
// already in chain-scoped format (4 colon-separated parts) are skipped.
func migrateTaskStatusBucket(db storage.Storage, chainID int64, legacyPrefix string, status avsproto.TaskStatus) (int, error) {
	keys, err := db.ListKeys(legacyPrefix)
	if err != nil {
		return 0, err
	}
	moved := 0
	for _, k := range keys {
		// Skip already-migrated entries (legacy bucket prefix overlaps the
		// chain-scoped bucket only at the literal "t:{status}:" stem, never at
		// the leading "t:{chainID}:" form, so ListKeys here returns just the
		// legacy stragglers — but we still guard explicitly).
		if isChainScopedTaskKey(k) {
			continue
		}
		// Legacy: t:{status}:{taskID} → parts[2] is the taskID
		parts := strings.SplitN(k, ":", 3)
		if len(parts) != 3 {
			return moved, fmt.Errorf("malformed legacy task-status key: %s", k)
		}
		taskID := parts[2]
		dst := storageschema.ChainWorkflowStorageKey(chainID, taskID, status)
		if err := db.Move([]byte(k), dst); err != nil {
			return moved, fmt.Errorf("moving %s → %s: %w", k, string(dst), err)
		}
		moved++
	}
	return moved, nil
}

func migrateUserTaskKeys(db storage.Storage, chainID int64) (int, error) {
	keys, err := db.ListKeys("u:")
	if err != nil {
		return 0, err
	}
	moved := 0
	for _, k := range keys {
		if isChainScopedUserKey(k) {
			continue
		}
		// Legacy: u:{owner}:{wallet}:{taskID}
		parts := strings.Split(k, ":")
		if len(parts) != 4 {
			return moved, fmt.Errorf("malformed legacy user-task key: %s", k)
		}
		owner, wallet, taskID := parts[1], parts[2], parts[3]
		dst := []byte(fmt.Sprintf("u:%d:%s:%s:%s", chainID, owner, wallet, taskID))
		if err := db.Move([]byte(k), dst); err != nil {
			return moved, fmt.Errorf("moving %s → %s: %w", k, string(dst), err)
		}
		moved++
	}
	return moved, nil
}

func migrateExecutionHistoryKeys(db storage.Storage, chainID int64) (int, error) {
	keys, err := db.ListKeys("history:")
	if err != nil {
		return 0, err
	}
	moved := 0
	for _, k := range keys {
		if isChainScopedExecutionKey(k) {
			continue
		}
		// Legacy: history:{taskID}:{executionID}
		parts := strings.SplitN(k, ":", 3)
		if len(parts) != 3 {
			return moved, fmt.Errorf("malformed legacy execution-history key: %s", k)
		}
		taskID, executionID := parts[1], parts[2]
		dst := []byte(fmt.Sprintf("history:%d:%s:%s", chainID, taskID, executionID))
		if err := db.Move([]byte(k), dst); err != nil {
			return moved, fmt.Errorf("moving %s → %s: %w", k, string(dst), err)
		}
		moved++
	}
	return moved, nil
}

// Chain-scoped key discriminators. Local to this package so we don't depend on
// the taskengine parsers and create a circular import (migrations is imported
// by aggregator, which transitively imports taskengine).

func isChainScopedTaskKey(k string) bool {
	// t:{chainID}:{status}:{taskID} has 4 parts; legacy t:{status}:{taskID} has 3.
	return strings.HasPrefix(k, "t:") && strings.Count(k, ":") >= 3
}

func isChainScopedUserKey(k string) bool {
	// u:{chainID}:{owner}:{wallet}:{taskID} has 5 parts; legacy 4.
	return strings.HasPrefix(k, "u:") && strings.Count(k, ":") >= 4
}

func isChainScopedExecutionKey(k string) bool {
	// history:{chainID}:{taskID}:{executionID} has 4 parts; legacy 3.
	return strings.HasPrefix(k, "history:") && strings.Count(k, ":") >= 3
}

// NewChainScopedKeysMigration returns a Migration that closes over chainID so
// the aggregator can register it from its startup path where the chain is
// known. The migration name embeds today's date for the standard ordering.
func NewChainScopedKeysMigration(chainID int64) migrator.Migration {
	return migrator.Migration{
		Name: "20260524-chain-scope-task-keys",
		Function: func(db storage.Storage) (int, error) {
			return MigrateKeysToChainScoped(db, chainID)
		},
	}
}
