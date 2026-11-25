package aggregator

import (
	"bufio"
	"fmt"
	"net"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/require"
)

// `disable <taskID>` pauses a workflow without its owner's credentials: the
// stored status becomes Disabled, and a second call reports it is no longer
// an active task.
func TestReplDisablePausesWorkflow(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))
	engine := taskengine.New(db, testutil.GetAggregatorConfig(), nil, testutil.GetLogger())

	user := testutil.TestUser1()
	req := testutil.RestTask()
	testutil.SetTaskSettings(req, "rest_task", user.SmartAccountAddress.Hex())
	task, err := engine.CreateWorkflow(user, req)
	require.NoError(t, err)

	agg := &Aggregator{logger: logger.NewNoOpLogger(), db: db, engine: engine}
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close() })
	go handleConnection(agg, server)
	out := bufio.NewReader(client)
	readUntil(t, client, out, "> ")
	send := func(cmd string) string {
		t.Helper()
		_, err := fmt.Fprintln(client, cmd)
		require.NoError(t, err)
		return readUntil(t, client, out, "> ")
	}

	require.Contains(t, send("disable"), "Usage: disable <taskID>")
	require.Contains(t, send("disable "+task.Id), "disabled "+task.Id)

	stored, err := engine.GetWorkflowByID(task.Id)
	require.NoError(t, err)
	require.Equal(t, avsproto.TaskStatus_Disabled, stored.Status)

	require.Contains(t, send("disable "+task.Id), "was not disabled")
}
