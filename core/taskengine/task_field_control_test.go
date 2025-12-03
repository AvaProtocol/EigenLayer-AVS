package taskengine

import (
	"fmt"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
)

func TestListTasksFieldControl(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	defer n.Stop()

	user := testutil.TestUser1()

	// Get a wallet for the user
	walletResp, err := n.GetWallet(user, &avsproto.GetWalletReq{
		Salt: "12345",
	})
	assert.NoError(t, err)
	smartWalletAddress := walletResp.Address

	// Create a test task with nodes and edges
	taskReq := testutil.RestTask()
	taskReq.Name = "test_task_field_control"
	taskReq.SmartWalletAddress = smartWalletAddress

	// Ensure the task has nodes and edges for testing
	assert.NotEmpty(t, taskReq.Nodes, "Test task should have nodes")
	assert.NotEmpty(t, taskReq.Edges, "Test task should have edges")

	_, err = n.CreateTask(user, taskReq)
	assert.NoError(t, err)

	t.Run("Default fields only (no nodes/edges)", func(t *testing.T) {
		resp, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
			SmartWalletAddress: []string{smartWalletAddress},
			// No field control flags set - should exclude expensive fields
		})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, 1)

		task := resp.Items[0]
		assert.Equal(t, "test_task_field_control", task.Name)
		assert.Equal(t, smartWalletAddress, task.SmartWalletAddress)
		assert.NotEmpty(t, task.Id)
		assert.Equal(t, user.Address.Hex(), task.Owner)

		// Expensive fields should be empty when not requested
		assert.Empty(t, task.Nodes, "Nodes should be empty when not requested")
		assert.Empty(t, task.Edges, "Edges should be empty when not requested")

		// Basic fields should be populated
		assert.Equal(t, avsproto.TaskStatus_Enabled, task.Status) // Should be Enabled when created
		assert.NotNil(t, task.Trigger)
	})

	t.Run("Include nodes only", func(t *testing.T) {
		resp, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
			SmartWalletAddress: []string{smartWalletAddress},
			IncludeNodes:       true,
		})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, 1)

		task := resp.Items[0]
		assert.Equal(t, "test_task_field_control", task.Name)

		// Nodes should be populated
		assert.NotEmpty(t, task.Nodes, "Nodes should be populated when requested")

		// Edges should still be empty
		assert.Empty(t, task.Edges, "Edges should be empty when not requested")
	})

	t.Run("Include edges only", func(t *testing.T) {
		resp, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
			SmartWalletAddress: []string{smartWalletAddress},
			IncludeEdges:       true,
		})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, 1)

		task := resp.Items[0]
		assert.Equal(t, "test_task_field_control", task.Name)

		// Edges should be populated
		assert.NotEmpty(t, task.Edges, "Edges should be populated when requested")

		// Nodes should still be empty
		assert.Empty(t, task.Nodes, "Nodes should be empty when not requested")
	})

	t.Run("Include both nodes and edges", func(t *testing.T) {
		resp, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
			SmartWalletAddress: []string{smartWalletAddress},
			IncludeNodes:       true,
			IncludeEdges:       true,
		})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, 1)

		task := resp.Items[0]
		assert.Equal(t, "test_task_field_control", task.Name)

		// Both nodes and edges should be populated
		assert.NotEmpty(t, task.Nodes, "Nodes should be populated when requested")
		assert.NotEmpty(t, task.Edges, "Edges should be populated when requested")

		// Verify the content matches the original task
		assert.Len(t, task.Nodes, len(taskReq.Nodes))
		assert.Len(t, task.Edges, len(taskReq.Edges))
	})

	t.Run("Pagination with field control", func(t *testing.T) {
		// Create additional tasks for pagination testing
		for i := 1; i <= 3; i++ {
			additionalTask := testutil.RestTask()
			additionalTask.Name = fmt.Sprintf("task_%d", i)
			additionalTask.SmartWalletAddress = smartWalletAddress
			_, err := n.CreateTask(user, additionalTask)
			assert.NoError(t, err)
		}

		resp, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
			SmartWalletAddress: []string{smartWalletAddress},
			Limit:              2,
			IncludeNodes:       true,
		})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, 2)

		// Verify field control works with pagination
		for _, task := range resp.Items {
			assert.NotEmpty(t, task.Name)
			assert.Equal(t, smartWalletAddress, task.SmartWalletAddress)
			assert.NotEmpty(t, task.Nodes, "Nodes should be populated when requested")
			assert.Empty(t, task.Edges, "Edges should be empty when not requested")
		}

		// Verify pagination info
		assert.NotNil(t, resp.PageInfo)
		assert.True(t, resp.PageInfo.HasNextPage)
	})
}

func TestTaskFieldControlPerformance(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	defer n.Stop()

	user := testutil.TestUser1()

	// Get a wallet for the user
	walletResp, err := n.GetWallet(user, &avsproto.GetWalletReq{
		Salt: "12345",
	})
	assert.NoError(t, err)
	smartWalletAddress := walletResp.Address

	// Create multiple tasks
	numTasks := 5
	for i := 0; i < numTasks; i++ {
		taskReq := testutil.RestTask()
		taskReq.Name = fmt.Sprintf("perf_task_%d", i)
		taskReq.SmartWalletAddress = smartWalletAddress
		_, err := n.CreateTask(user, taskReq)
		assert.NoError(t, err)
	}

	t.Run("Minimal fields for performance", func(t *testing.T) {
		resp, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
			SmartWalletAddress: []string{smartWalletAddress},
			// No expensive fields - fastest response
		})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, numTasks)

		// Verify only basic fields are populated
		for _, task := range resp.Items {
			assert.NotEmpty(t, task.Name)
			assert.Equal(t, smartWalletAddress, task.SmartWalletAddress)
			assert.NotEmpty(t, task.Id)
			assert.Equal(t, user.Address.Hex(), task.Owner)

			// Expensive fields should be empty
			assert.Empty(t, task.Nodes, "Nodes should be empty for performance")
			assert.Empty(t, task.Edges, "Edges should be empty for performance")
		}
	})

	t.Run("All fields for detailed view", func(t *testing.T) {
		resp, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
			SmartWalletAddress: []string{smartWalletAddress},
			IncludeNodes:       true,
			IncludeEdges:       true,
		})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, numTasks)

		// Verify all fields are populated
		for _, task := range resp.Items {
			assert.NotEmpty(t, task.Name)
			assert.Equal(t, smartWalletAddress, task.SmartWalletAddress)
			assert.NotEmpty(t, task.Id)
			assert.Equal(t, user.Address.Hex(), task.Owner)

			// Expensive fields should be populated
			assert.NotEmpty(t, task.Nodes, "Nodes should be populated when requested")
			assert.NotEmpty(t, task.Edges, "Edges should be populated when requested")
		}
	})
}

func TestTaskFieldControlConsistency(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	defer n.Stop()

	user := testutil.TestUser1()

	// Get a wallet for the user
	walletResp, err := n.GetWallet(user, &avsproto.GetWalletReq{
		Salt: "12345",
	})
	assert.NoError(t, err)
	smartWalletAddress := walletResp.Address

	// Create a test task
	taskReq := testutil.RestTask()
	taskReq.Name = "consistency_test_task"
	taskReq.SmartWalletAddress = smartWalletAddress

	createdTask, err := n.CreateTask(user, taskReq)
	assert.NoError(t, err)

	t.Run("ListTasks with full fields matches GetTask", func(t *testing.T) {
		// Get the task via GetTask (which returns full Task message)
		fullTask, err := n.GetTask(user, createdTask.Id)
		assert.NoError(t, err)

		fullTaskProto, err := fullTask.ToProtoBuf()
		assert.NoError(t, err)

		// Get the task via ListTasks with all fields included
		listResp, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
			SmartWalletAddress: []string{smartWalletAddress},
			IncludeNodes:       true,
			IncludeEdges:       true,
		})
		assert.NoError(t, err)
		assert.Len(t, listResp.Items, 1)

		listTask := listResp.Items[0]

		// Compare all fields
		assert.Equal(t, fullTaskProto.Id, listTask.Id)
		assert.Equal(t, fullTaskProto.Owner, listTask.Owner)
		assert.Equal(t, fullTaskProto.SmartWalletAddress, listTask.SmartWalletAddress)
		assert.Equal(t, fullTaskProto.Name, listTask.Name)
		assert.Equal(t, fullTaskProto.Status, listTask.Status)
		assert.Len(t, listTask.Nodes, len(fullTaskProto.Nodes))
		assert.Len(t, listTask.Edges, len(fullTaskProto.Edges))
	})

	t.Run("Field control doesn't affect basic fields", func(t *testing.T) {
		// Get task with no expensive fields
		minimalResp, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
			SmartWalletAddress: []string{smartWalletAddress},
		})
		assert.NoError(t, err)

		// Get task with all fields
		fullResp, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
			SmartWalletAddress: []string{smartWalletAddress},
			IncludeNodes:       true,
			IncludeEdges:       true,
		})
		assert.NoError(t, err)

		minimalTask := minimalResp.Items[0]
		fullTask := fullResp.Items[0]

		// Basic fields should be identical
		assert.Equal(t, fullTask.Id, minimalTask.Id)
		assert.Equal(t, fullTask.Owner, minimalTask.Owner)
		assert.Equal(t, fullTask.SmartWalletAddress, minimalTask.SmartWalletAddress)
		assert.Equal(t, fullTask.Name, minimalTask.Name)
		assert.Equal(t, fullTask.Status, minimalTask.Status)
		assert.Equal(t, fullTask.StartAt, minimalTask.StartAt)
		assert.Equal(t, fullTask.ExpiredAt, minimalTask.ExpiredAt)
		assert.Equal(t, fullTask.MaxExecution, minimalTask.MaxExecution)
		assert.Equal(t, fullTask.ExecutionCount, minimalTask.ExecutionCount)

		// Only expensive fields should differ
		assert.Empty(t, minimalTask.Nodes)
		assert.Empty(t, minimalTask.Edges)
		assert.NotEmpty(t, fullTask.Nodes)
		assert.NotEmpty(t, fullTask.Edges)
	})
}
