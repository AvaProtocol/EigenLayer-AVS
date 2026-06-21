package migrations

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	storageschema "github.com/AvaProtocol/EigenLayer-AVS/storage/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// disabledOwnershipTask builds a structurally-valid task carrying the persisted
// auto-disable bookkeeping the executor writes (LastValidationError +
// ConsecutiveValidationFailures).
func disabledOwnershipTask(id, owner, wallet string, chainID int64, lastErr string, failures uint32) *model.Workflow {
	w := blockTaskWorkflow(id, owner, wallet, chainID, true)
	w.LastValidationError = lastErr
	w.ConsecutiveValidationFailures = failures
	return w
}

func TestDeleteAutoDisabledInvalidTasks(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()
	const chainID = int64(11155111)
	const ownershipErr = "task smart wallet address does not belong to owner"

	// 1) Disabled + ownership error + at threshold → must be deleted. Valid
	//    config (passes ValidateWithError), proving we don't depend on it.
	disabled := disabledOwnershipTask("auto-disabled", "0xOwnerA", "0xWalletA", chainID, ownershipErr, autoDisableValidationFailureThreshold)
	require.NoError(t, disabled.ValidateWithError(), "fixture 1 must be structurally valid")
	seed(t, db, avsproto.TaskStatus_Disabled, disabled)
	// Leave a stale Enabled orphan to prove it gets cleaned up too.
	orphanBytes, _ := disabled.ToJSON()
	require.NoError(t, db.Set(storageschema.ChainWorkflowStorageKey(chainID, disabled.Id, avsproto.TaskStatus_Enabled), orphanBytes))

	// 2) Disabled + ownership error but below threshold → preserved.
	belowThreshold := disabledOwnershipTask("below-threshold", "0xOwnerB", "0xWalletB", chainID, ownershipErr, autoDisableValidationFailureThreshold-1)
	seed(t, db, avsproto.TaskStatus_Disabled, belowThreshold)

	// 3) Disabled with no permanent validation error (e.g. user-disabled) → preserved.
	userDisabled := disabledOwnershipTask("user-disabled", "0xOwnerC", "0xWalletC", chainID, "", 0)
	seed(t, db, avsproto.TaskStatus_Disabled, userDisabled)

	// 4) Failed + ownership error + at threshold → untouched (wrong status; the
	//    Failed cohort belongs to DeleteInvalidFailedTasks, not this sweep).
	failed := disabledOwnershipTask("failed-ownership", "0xOwnerD", "0xWalletD", chainID, ownershipErr, autoDisableValidationFailureThreshold)
	seed(t, db, avsproto.TaskStatus_Failed, failed)

	// 5) Enabled → untouched.
	enabled := blockTaskWorkflow("valid-enabled", "0xOwnerE", "0xWalletE", chainID, true)
	seed(t, db, avsproto.TaskStatus_Enabled, enabled)

	n, err := DeleteAutoDisabledInvalidTasks(db)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the at-threshold auto-disabled task should be deleted")

	// Deleted task: every key gone (Disabled row, Enabled orphan, user index).
	for _, k := range [][]byte{
		storageschema.ChainWorkflowStorageKey(chainID, disabled.Id, avsproto.TaskStatus_Disabled),
		storageschema.ChainWorkflowStorageKey(chainID, disabled.Id, avsproto.TaskStatus_Enabled),
		userIndexKey(disabled),
	} {
		ex, _ := db.Exist(k)
		assert.False(t, ex, "auto-disabled task key must be deleted: %s", string(k))
	}

	// Preserved fixtures.
	for _, w := range []*model.Workflow{belowThreshold, userDisabled} {
		ex, _ := db.Exist(storageschema.ChainWorkflowStorageKey(chainID, w.Id, avsproto.TaskStatus_Disabled))
		assert.True(t, ex, "Disabled task below cohort must be preserved: %s", w.Id)
	}
	fex, _ := db.Exist(storageschema.ChainWorkflowStorageKey(chainID, failed.Id, avsproto.TaskStatus_Failed))
	assert.True(t, fex, "Failed task must be untouched by this migration")
	eex, _ := db.Exist(storageschema.ChainWorkflowStorageKey(chainID, enabled.Id, avsproto.TaskStatus_Enabled))
	assert.True(t, eex, "Enabled task must be untouched")

	// Idempotent: a second run deletes nothing.
	n2, err := DeleteAutoDisabledInvalidTasks(db)
	require.NoError(t, err)
	assert.Equal(t, 0, n2, "second run must be a no-op")
}
