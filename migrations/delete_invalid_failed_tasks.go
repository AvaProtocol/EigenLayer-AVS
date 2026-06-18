package migrations

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	storageschema "github.com/AvaProtocol/EigenLayer-AVS/storage/schema"
	"google.golang.org/protobuf/encoding/protojson"
)

// DeleteInvalidFailedTasks removes workflow records that are BOTH in Failed
// status AND still fail config validation — the legacy cohort that the
// boot-time invalid-task scan (DetectAndHandleInvalidTasks) retired: invalid
// node names like "send eth", or trigger configs orphaned by an older proto
// field migration. They can never become valid and persist only as dead rows.
//
// Safety properties:
//   - Only Failed rows (t:<chain>:f:<id>) are considered; Enabled/Completed/
//     Disabled/Running tasks are never touched.
//   - A Failed row is deleted ONLY if ValidateWithError still reports it
//     invalid. A task that is Failed for a legitimate runtime reason (valid
//     config) is preserved.
//   - Rows whose JSON can't be decoded are left in place — we never delete
//     data we can't interpret.
//   - The migrator takes a full DB backup before this runs, and records the
//     migration so it executes exactly once.
//
// For each deleted task we remove every key it owns: the Failed status row,
// the user index (u:<chain>:<owner>:<wallet>:<id>), and — defensively — any
// lingering Enabled orphan (t:<chain>:a:<id>) left by the pre-fix scan.
func DeleteInvalidFailedTasks(db storage.Storage) (int, error) {
	failedToken := storageschema.WorkflowStatusToStorageKey(avsproto.TaskStatus_Failed)

	items, err := db.GetByPrefix([]byte("t:"))
	if err != nil {
		return 0, err
	}

	deleted := 0
	for _, it := range items {
		// Match Failed workflow rows: t:<chain>:f:<id>. Task IDs are ULIDs
		// (no colons), so a 4-way split is exact.
		parts := strings.SplitN(string(it.Key), ":", 4)
		if len(parts) != 4 || parts[0] != "t" || parts[2] != failedToken {
			continue
		}
		chainID, perr := strconv.ParseInt(parts[1], 10, 64)
		if perr != nil {
			continue
		}

		task := &model.Workflow{Task: &avsproto.Task{}}
		if uerr := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(it.Value, task); uerr != nil {
			// Undecodable record — leave it alone rather than risk deleting
			// something we can't interpret.
			continue
		}

		// Only delete genuinely-invalid tasks (the orphaned cohort). A Failed
		// task with a valid config is a legitimate runtime failure — keep it.
		if task.ValidateWithError() == nil {
			continue
		}

		// Delete the Failed status row (fail loudly — this key definitely
		// exists, so an error here is real).
		if derr := db.Delete(it.Key); derr != nil {
			return deleted, fmt.Errorf("delete failed-status key %q: %w", string(it.Key), derr)
		}
		// Delete the user index and any Enabled orphan. These may be absent
		// (Delete on a missing key is a no-op), so best-effort is fine.
		userKey := fmt.Sprintf("u:%d:%s:%s:%s", chainID,
			strings.ToLower(task.GetOwner()), strings.ToLower(task.GetSmartWalletAddress()), task.GetId())
		_ = db.Delete([]byte(userKey))
		_ = db.Delete(storageschema.ChainWorkflowStorageKey(chainID, task.GetId(), avsproto.TaskStatus_Enabled))

		deleted++
	}

	return deleted, nil
}
