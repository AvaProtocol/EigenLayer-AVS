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

// autoDisableValidationFailureThreshold mirrors validationFailureDisableThreshold
// in core/taskengine/executor.go — the number of consecutive permanent
// validation failures the executor tolerates before flipping a task to Disabled.
// Kept as a local copy so this migration does not import the task engine; it
// must stay in sync with the executor.
const autoDisableValidationFailureThreshold uint32 = 10

// permanentValidationErrorPrefixes mirrors the identically-named slice in
// core/taskengine/executor.go. A task auto-disabled by the executor records the
// triggering execution.Error in Task.LastValidationError; these prefixes are the
// permanent (won't self-resolve) reasons that drive an auto-disable. Matching on
// them lets us reconstruct exactly the auto-disabled cohort. Keep in sync with
// the executor.
var permanentValidationErrorPrefixes = []string{
	"invalid or missing task smart wallet address",
	"task smart wallet address does not belong to owner",
	"failed to create VM:",
}

func isPermanentValidationError(errorMsg string) bool {
	for _, prefix := range permanentValidationErrorPrefixes {
		if strings.HasPrefix(errorMsg, prefix) {
			return true
		}
	}
	return false
}

// DeleteAutoDisabledInvalidTasks removes workflow records that the executor
// auto-disabled after validationFailureDisableThreshold consecutive *permanent*
// validation failures — overwhelmingly "task smart wallet address does not
// belong to owner" (see EIGENLAYER-AVS-1X..28). These tasks are structurally
// valid (they pass ValidateWithError, so the Failed-cohort migration
// DeleteInvalidFailedTasks deliberately leaves them alone) but can never
// execute: the wallet/owner relationship is permanently wrong. The executor
// already fired its one-shot Sentry alert and flipped them to Disabled; this
// sweep removes the dead rows they leave behind. The auto-disable warning and
// Sentry event are untouched — future breakage still alerts; this only cleans up
// after it.
//
// Cohort match (reconstructs exactly what the executor's auto-disable produced):
//   - status Disabled (t:<chain>:i:<id>)
//   - LastValidationError matches a permanent validation prefix
//   - ConsecutiveValidationFailures >= autoDisableValidationFailureThreshold
//
// Safety properties:
//   - Only Disabled rows are considered; Enabled/Completed/Failed/Running tasks
//     are never touched.
//   - A task a user manually disabled won't match: it carries no permanent
//     LastValidationError with >= threshold consecutive failures.
//   - Rows whose JSON can't be decoded are left in place — we never delete data
//     we can't interpret.
//   - The migrator takes a full DB backup before this runs, and records the
//     migration so it executes exactly once. Idempotent: re-running finds none.
//
// For each deleted task we remove every key it owns: the Disabled status row,
// the user index (u:<chain>:<owner>:<wallet>:<id>), and — defensively — any
// lingering Enabled orphan (t:<chain>:a:<id>).
func DeleteAutoDisabledInvalidTasks(db storage.Storage) (int, error) {
	disabledToken := storageschema.WorkflowStatusToStorageKey(avsproto.TaskStatus_Disabled)

	items, err := db.GetByPrefix([]byte("t:"))
	if err != nil {
		return 0, err
	}

	deleted := 0
	for _, it := range items {
		// Match Disabled workflow rows: t:<chain>:i:<id>. Task IDs are ULIDs
		// (no colons), so a 4-way split is exact.
		parts := strings.SplitN(string(it.Key), ":", 4)
		if len(parts) != 4 || parts[0] != "t" || parts[2] != disabledToken {
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

		// Only delete the auto-disabled-for-permanent-validation cohort. A task
		// disabled for any other reason (e.g. a user toggling it off) won't
		// carry a permanent LastValidationError with >= threshold failures.
		if !isPermanentValidationError(task.GetLastValidationError()) ||
			task.GetConsecutiveValidationFailures() < autoDisableValidationFailureThreshold {
			continue
		}

		// Delete the Disabled status row (fail loudly — this key definitely
		// exists, so an error here is real).
		if derr := db.Delete(it.Key); derr != nil {
			return deleted, fmt.Errorf("delete disabled-status key %q: %w", string(it.Key), derr)
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
