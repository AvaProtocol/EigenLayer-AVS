package migrations

import (
	"fmt"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	storageschema "github.com/AvaProtocol/EigenLayer-AVS/storage/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func userIndexKey(w *model.Workflow) []byte {
	return []byte(fmt.Sprintf("u:%d:%s:%s:%s", w.ChainId,
		strings.ToLower(w.Owner), strings.ToLower(w.SmartWalletAddress), w.Id))
}

// seed writes a task under its status key plus the matching user index.
func seed(t *testing.T, db storage.Storage, status avsproto.TaskStatus, w *model.Workflow) {
	t.Helper()
	w.Status = status
	b, err := w.ToJSON()
	require.NoError(t, err)
	require.NoError(t, db.Set(storageschema.ChainWorkflowStorageKey(w.ChainId, w.Id, status), b))
	require.NoError(t, db.Set(userIndexKey(w), []byte("ref")))
}

func blockTaskWorkflow(id, owner, wallet string, chainID int64, withConfig bool) *model.Workflow {
	block := &avsproto.BlockTrigger{}
	if withConfig {
		block.Config = &avsproto.BlockTrigger_Config{Interval: 1}
	}
	return &model.Workflow{Task: &avsproto.Task{
		Id: id, ChainId: chainID, Owner: owner, SmartWalletAddress: wallet,
		Trigger: &avsproto.TaskTrigger{
			Name:        "trigger1",
			TriggerType: &avsproto.TaskTrigger_Block{Block: block},
		},
	}}
}

func TestDeleteInvalidFailedTasks(t *testing.T) {
	db := testutil.TestMustDB()
	defer db.Close()
	const chainID = int64(11155111)

	// 1) Invalid + Failed → must be deleted (block trigger with nil config).
	invalid := blockTaskWorkflow("invalid-failed", "0xOwnerA", "0xWalletA", chainID, false)
	require.Error(t, invalid.ValidateWithError(), "fixture 1 must be invalid")
	seed(t, db, avsproto.TaskStatus_Failed, invalid)
	// Also leave a stale Enabled orphan to prove it gets cleaned up too.
	orphanBytes, _ := invalid.ToJSON()
	require.NoError(t, db.Set(storageschema.ChainWorkflowStorageKey(chainID, invalid.Id, avsproto.TaskStatus_Enabled), orphanBytes))

	// 2) Valid + Failed → must be preserved (legitimate runtime failure).
	validFailed := blockTaskWorkflow("valid-failed", "0xOwnerB", "0xWalletB", chainID, true)
	require.NoError(t, validFailed.ValidateWithError(), "fixture 2 must be valid")
	seed(t, db, avsproto.TaskStatus_Failed, validFailed)

	// 3) Valid + Enabled → must be untouched.
	enabled := blockTaskWorkflow("valid-enabled", "0xOwnerC", "0xWalletC", chainID, true)
	seed(t, db, avsproto.TaskStatus_Enabled, enabled)

	n, err := DeleteInvalidFailedTasks(db)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the invalid Failed task should be deleted")

	// Invalid task: every key gone.
	for _, k := range [][]byte{
		storageschema.ChainWorkflowStorageKey(chainID, invalid.Id, avsproto.TaskStatus_Failed),
		storageschema.ChainWorkflowStorageKey(chainID, invalid.Id, avsproto.TaskStatus_Enabled),
		userIndexKey(invalid),
	} {
		ex, _ := db.Exist(k)
		assert.False(t, ex, "invalid task key must be deleted: %s", string(k))
	}

	// Valid Failed task: preserved (status row + index).
	vex, _ := db.Exist(storageschema.ChainWorkflowStorageKey(chainID, validFailed.Id, avsproto.TaskStatus_Failed))
	assert.True(t, vex, "valid Failed task must be preserved")
	vuex, _ := db.Exist(userIndexKey(validFailed))
	assert.True(t, vuex, "valid Failed task user index must be preserved")

	// Enabled task: untouched.
	eex, _ := db.Exist(storageschema.ChainWorkflowStorageKey(chainID, enabled.Id, avsproto.TaskStatus_Enabled))
	assert.True(t, eex, "enabled task must be untouched")

	// Idempotent: a second run deletes nothing.
	n2, err := DeleteInvalidFailedTasks(db)
	require.NoError(t, err)
	assert.Equal(t, 0, n2, "second run must be a no-op")
}
