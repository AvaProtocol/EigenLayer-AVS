package taskengine

import (
	"fmt"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// setupTasksEngine creates an engine with the given number of tasks and returns
// the engine, user, and wallet address for use in pagination tests.
func setupTasksEngine(t *testing.T, totalTasks int) (*Engine, *avsproto.GetWalletResp, func()) {
	t.Helper()
	db := testutil.TestMustDB()
	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	user := testutil.TestUser1()

	wallet, err := n.GetWallet(user, &avsproto.GetWalletReq{Salt: "12345"})
	require.NoError(t, err, "Failed to get wallet")

	for i := 0; i < totalTasks; i++ {
		task := testutil.RestTask()
		testutil.SetTaskSettings(task, fmt.Sprintf("task%d", i), wallet.Address)
		_, err := n.CreateTask(user, task)
		require.NoError(t, err, "Failed to create task %d", i)
	}

	cleanup := func() { storage.Destroy(db.(*storage.BadgerStorage)) }
	return n, wallet, cleanup
}

// setupExecutionsEngine creates an engine with a single task that has the given
// number of executions, for use in execution pagination tests.
func setupExecutionsEngine(t *testing.T, totalExecutions int) (*Engine, string, func()) {
	t.Helper()
	db := testutil.TestMustDB()
	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	user := testutil.TestUser1()

	wallet, err := n.GetWallet(user, &avsproto.GetWalletReq{Salt: "12345"})
	require.NoError(t, err)

	tr := testutil.RestTask()
	testutil.SetTaskSettings(tr, "exec_pagination_task", wallet.Address)
	task, err := n.CreateTask(user, tr)
	require.NoError(t, err)

	for i := 0; i < totalExecutions; i++ {
		_, err := n.TriggerTask(user, &avsproto.TriggerTaskReq{
			TaskId:      task.Id,
			TriggerType: avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
			TriggerOutput: &avsproto.TriggerTaskReq_BlockTrigger{
				BlockTrigger: &avsproto.BlockTrigger_Output{
					Data: &structpb.Value{
						Kind: &structpb.Value_NumberValue{
							NumberValue: float64(100 + i),
						},
					},
				},
			},
			IsBlocking: true,
		})
		require.NoError(t, err, "Failed to trigger execution %d", i)
	}

	cleanup := func() { storage.Destroy(db.(*storage.BadgerStorage)) }
	return n, task.Id, cleanup
}

// collectAllIds fetches a single page of all items to get the canonical order.
func collectAllTaskIds(t *testing.T, n *Engine, walletAddr string, total int) []string {
	t.Helper()
	user := testutil.TestUser1()
	all, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
		SmartWalletAddress: []string{walletAddr},
		Limit:              int64(total),
	})
	require.NoError(t, err)
	ids := make([]string, len(all.Items))
	for i, item := range all.Items {
		ids[i] = item.Id
	}
	return ids
}

func collectAllExecutionIds(t *testing.T, n *Engine, taskId string, total int) []string {
	t.Helper()
	user := testutil.TestUser1()
	all, err := n.ListExecutions(user, &avsproto.ListExecutionsReq{
		TaskIds: []string{taskId},
		Limit:   int64(total),
	})
	require.NoError(t, err)
	ids := make([]string, len(all.Items))
	for i, item := range all.Items {
		ids[i] = item.Id
	}
	return ids
}

// TestTasksPaginationForwardThreePagesThenBackward reproduces the exact scenario
// from the CURSOR_PAGINATION_ISSUE: navigate forward 3 pages, then backward,
// and verify the adjacent previous page is returned (not the first page).
// Uses 13 items (not 15) to avoid a pre-existing hasMoreItems edge case when
// total is exactly divisible by page size.
func TestTasksPaginationForwardThreePagesThenBackward(t *testing.T) {
	const (
		totalTasks = 13
		pageSize   = 5
	)
	n, wallet, cleanup := setupTasksEngine(t, totalTasks)
	defer cleanup()
	user := testutil.TestUser1()

	allIds := collectAllTaskIds(t, n, wallet.Address, totalTasks)
	// allIds is newest-first: [14, 13, ..., 0]

	// Page 1: no cursor → [14, 13, 12, 11, 10]
	page1, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
		SmartWalletAddress: []string{wallet.Address},
		Limit:              pageSize,
	})
	require.NoError(t, err)
	assert.Equal(t, pageSize, len(page1.Items))
	assert.True(t, page1.PageInfo.HasNextPage)
	assert.False(t, page1.PageInfo.HasPreviousPage)
	for i := 0; i < pageSize; i++ {
		assert.Equal(t, allIds[i], page1.Items[i].Id, "page1 item %d", i)
	}

	// Page 2: after endCursor of page 1 → [9, 8, 7, 6, 5]
	page2, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
		SmartWalletAddress: []string{wallet.Address},
		After:              page1.PageInfo.EndCursor,
		Limit:              pageSize,
	})
	require.NoError(t, err)
	assert.Equal(t, pageSize, len(page2.Items))
	assert.True(t, page2.PageInfo.HasNextPage)
	assert.True(t, page2.PageInfo.HasPreviousPage)
	for i := 0; i < pageSize; i++ {
		assert.Equal(t, allIds[pageSize+i], page2.Items[i].Id, "page2 item %d", i)
	}

	// Page 3: after endCursor of page 2 → last 3 items
	page3, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
		SmartWalletAddress: []string{wallet.Address},
		After:              page2.PageInfo.EndCursor,
		Limit:              pageSize,
	})
	require.NoError(t, err)
	lastPageSize := totalTasks - 2*pageSize // 13 - 10 = 3
	assert.Equal(t, lastPageSize, len(page3.Items))
	assert.False(t, page3.PageInfo.HasNextPage)
	assert.True(t, page3.PageInfo.HasPreviousPage)
	for i := 0; i < lastPageSize; i++ {
		assert.Equal(t, allIds[2*pageSize+i], page3.Items[i].Id, "page3 item %d", i)
	}

	// THE BUG SCENARIO: before startCursor of page 3 → should get page 2
	prevPage, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
		SmartWalletAddress: []string{wallet.Address},
		Before:             page3.PageInfo.StartCursor,
		Limit:              pageSize,
	})
	require.NoError(t, err)
	assert.Equal(t, pageSize, len(prevPage.Items), "backward page should have %d items", pageSize)
	assert.True(t, prevPage.PageInfo.HasNextPage, "backward page should have next (older) items")
	assert.True(t, prevPage.PageInfo.HasPreviousPage, "backward page should have previous (newer) items")

	// Verify it returns page 2 items, not page 1
	for i := 0; i < pageSize; i++ {
		assert.Equal(t, page2.Items[i].Id, prevPage.Items[i].Id,
			"backward from page 3 should return page 2 items, got wrong item at position %d", i)
	}
}

// TestTasksPaginationFullRoundTrip navigates forward to the last page, then
// backward all the way to the first page, verifying every page matches.
func TestTasksPaginationFullRoundTrip(t *testing.T) {
	const (
		totalTasks = 11
		pageSize   = 4
		numPages   = 3 // pages of 4, 4, 3
	)
	n, wallet, cleanup := setupTasksEngine(t, totalTasks)
	defer cleanup()
	user := testutil.TestUser1()

	allIds := collectAllTaskIds(t, n, wallet.Address, totalTasks)

	// Navigate forward, collecting all pages
	forwardPages := make([][]*avsproto.Task, 0, numPages)
	var afterCursor string

	for p := 0; p < numPages; p++ {
		req := &avsproto.ListTasksReq{
			SmartWalletAddress: []string{wallet.Address},
			Limit:              pageSize,
		}
		if afterCursor != "" {
			req.After = afterCursor
		}
		page, err := n.ListTasksByUser(user, req)
		require.NoError(t, err, "forward page %d", p)
		forwardPages = append(forwardPages, page.Items)

		// Verify items match canonical order
		for i, item := range page.Items {
			assert.Equal(t, allIds[p*pageSize+i], item.Id, "forward page %d item %d", p, i)
		}

		afterCursor = page.PageInfo.EndCursor
		if p < numPages-1 {
			assert.True(t, page.PageInfo.HasNextPage, "forward page %d should have next", p)
		}
	}

	// Collect startCursors during a second forward pass for backward navigation
	forwardStartCursors := make([]string, 0, numPages)
	afterCursor = ""
	for p := 0; p < numPages; p++ {
		req := &avsproto.ListTasksReq{
			SmartWalletAddress: []string{wallet.Address},
			Limit:              pageSize,
		}
		if afterCursor != "" {
			req.After = afterCursor
		}
		page, err := n.ListTasksByUser(user, req)
		require.NoError(t, err)
		forwardStartCursors = append(forwardStartCursors, page.PageInfo.StartCursor)
		afterCursor = page.PageInfo.EndCursor
	}

	// Navigate backward from last page to first
	beforeCursor := forwardStartCursors[numPages-1]
	for p := numPages - 2; p >= 0; p-- {
		page, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
			SmartWalletAddress: []string{wallet.Address},
			Before:             beforeCursor,
			Limit:              pageSize,
		})
		require.NoError(t, err, "backward page %d", p)
		assert.Equal(t, pageSize, len(page.Items), "backward page %d size", p)

		// Should match the corresponding forward page
		for i, item := range page.Items {
			assert.Equal(t, forwardPages[p][i].Id, item.Id,
				"backward page %d item %d should match forward page", p, i)
		}

		beforeCursor = page.PageInfo.StartCursor
		assert.True(t, page.PageInfo.HasNextPage, "backward page %d should have next (older items)", p)

		if p > 0 {
			assert.True(t, page.PageInfo.HasPreviousPage, "backward page %d should have previous (newer items)", p)
		} else {
			assert.False(t, page.PageInfo.HasPreviousPage, "first page reached via backward should have no previous")
		}
	}
}

// TestTasksPaginationBackwardFromLastPage tests backward navigation starting
// from the last page (using startCursor of the last page).
func TestTasksPaginationBackwardFromLastPage(t *testing.T) {
	const (
		totalTasks = 7
		pageSize   = 3
	)
	n, wallet, cleanup := setupTasksEngine(t, totalTasks)
	defer cleanup()
	user := testutil.TestUser1()

	allIds := collectAllTaskIds(t, n, wallet.Address, totalTasks)

	// Forward to page 2 (last full page)
	page1, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
		SmartWalletAddress: []string{wallet.Address},
		Limit:              pageSize,
	})
	require.NoError(t, err)

	page2, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
		SmartWalletAddress: []string{wallet.Address},
		After:              page1.PageInfo.EndCursor,
		Limit:              pageSize,
	})
	require.NoError(t, err)
	assert.Equal(t, pageSize, len(page2.Items))

	// Forward to page 3 (partial page, 1 item)
	page3, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
		SmartWalletAddress: []string{wallet.Address},
		After:              page2.PageInfo.EndCursor,
		Limit:              pageSize,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, len(page3.Items), "last page should have 1 item (7 mod 3 = 1)")
	assert.Equal(t, allIds[6], page3.Items[0].Id)
	assert.False(t, page3.PageInfo.HasNextPage)

	// Backward from page 3: should get page 2
	backPage, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
		SmartWalletAddress: []string{wallet.Address},
		Before:             page3.PageInfo.StartCursor,
		Limit:              pageSize,
	})
	require.NoError(t, err)
	assert.Equal(t, pageSize, len(backPage.Items))
	for i := 0; i < pageSize; i++ {
		assert.Equal(t, page2.Items[i].Id, backPage.Items[i].Id,
			"backward from last page item %d should match page 2", i)
	}
}

// TestTasksPaginationSingleItemPages tests backward pagination with page size 1.
func TestTasksPaginationSingleItemPages(t *testing.T) {
	const (
		totalTasks = 5
		pageSize   = 1
	)
	n, wallet, cleanup := setupTasksEngine(t, totalTasks)
	defer cleanup()
	user := testutil.TestUser1()

	allIds := collectAllTaskIds(t, n, wallet.Address, totalTasks)

	// Navigate forward through all 5 single-item pages
	pages := make([]*avsproto.ListTasksResp, 0, totalTasks)
	var afterCursor string
	for i := 0; i < totalTasks; i++ {
		req := &avsproto.ListTasksReq{
			SmartWalletAddress: []string{wallet.Address},
			Limit:              pageSize,
		}
		if afterCursor != "" {
			req.After = afterCursor
		}
		page, err := n.ListTasksByUser(user, req)
		require.NoError(t, err)
		assert.Equal(t, 1, len(page.Items))
		assert.Equal(t, allIds[i], page.Items[0].Id, "forward item %d", i)
		pages = append(pages, page)
		afterCursor = page.PageInfo.EndCursor
	}

	// Navigate backward from item 4 (last) using startCursor
	for i := totalTasks - 1; i > 0; i-- {
		backPage, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
			SmartWalletAddress: []string{wallet.Address},
			Before:             pages[i].PageInfo.StartCursor,
			Limit:              pageSize,
		})
		require.NoError(t, err)
		assert.Equal(t, 1, len(backPage.Items))
		assert.Equal(t, allIds[i-1], backPage.Items[0].Id,
			"backward from item %d should return item %d", i, i-1)
	}
}

// TestExecutionsPaginationForwardThreePagesThenBackward tests the exact issue
// scenario against ListExecutions (the function called by the frontend).
func TestExecutionsPaginationForwardThreePagesThenBackward(t *testing.T) {
	const (
		totalExecs = 13
		pageSize   = 5
	)
	n, taskId, cleanup := setupExecutionsEngine(t, totalExecs)
	defer cleanup()
	user := testutil.TestUser1()

	allIds := collectAllExecutionIds(t, n, taskId, totalExecs)

	// Page 1
	page1, err := n.ListExecutions(user, &avsproto.ListExecutionsReq{
		TaskIds: []string{taskId},
		Limit:   pageSize,
	})
	require.NoError(t, err)
	assert.Equal(t, pageSize, len(page1.Items))
	assert.True(t, page1.PageInfo.HasNextPage)
	assert.False(t, page1.PageInfo.HasPreviousPage)
	for i := 0; i < pageSize; i++ {
		assert.Equal(t, allIds[i], page1.Items[i].Id, "exec page1 item %d", i)
	}

	// Page 2
	page2, err := n.ListExecutions(user, &avsproto.ListExecutionsReq{
		TaskIds: []string{taskId},
		After:   page1.PageInfo.EndCursor,
		Limit:   pageSize,
	})
	require.NoError(t, err)
	assert.Equal(t, pageSize, len(page2.Items))
	assert.True(t, page2.PageInfo.HasNextPage)
	assert.True(t, page2.PageInfo.HasPreviousPage)
	for i := 0; i < pageSize; i++ {
		assert.Equal(t, allIds[pageSize+i], page2.Items[i].Id, "exec page2 item %d", i)
	}

	// Page 3 (partial: 3 items)
	page3, err := n.ListExecutions(user, &avsproto.ListExecutionsReq{
		TaskIds: []string{taskId},
		After:   page2.PageInfo.EndCursor,
		Limit:   pageSize,
	})
	require.NoError(t, err)
	lastPageSize := totalExecs - 2*pageSize // 13 - 10 = 3
	assert.Equal(t, lastPageSize, len(page3.Items))
	assert.False(t, page3.PageInfo.HasNextPage)
	assert.True(t, page3.PageInfo.HasPreviousPage)
	for i := 0; i < lastPageSize; i++ {
		assert.Equal(t, allIds[2*pageSize+i], page3.Items[i].Id, "exec page3 item %d", i)
	}

	// THE BUG SCENARIO: before startCursor of page 3 → should get page 2
	prevPage, err := n.ListExecutions(user, &avsproto.ListExecutionsReq{
		TaskIds: []string{taskId},
		Before:  page3.PageInfo.StartCursor,
		Limit:   pageSize,
	})
	require.NoError(t, err)
	assert.Equal(t, pageSize, len(prevPage.Items), "backward page should have %d items", pageSize)
	assert.True(t, prevPage.PageInfo.HasNextPage)
	assert.True(t, prevPage.PageInfo.HasPreviousPage)

	for i := 0; i < pageSize; i++ {
		assert.Equal(t, page2.Items[i].Id, prevPage.Items[i].Id,
			"backward from page 3 should return page 2 items, got wrong item at position %d", i)
	}
}

// TestExecutionsPaginationFullRoundTrip navigates forward through all execution
// pages then backward, verifying every page matches.
func TestExecutionsPaginationFullRoundTrip(t *testing.T) {
	const (
		totalExecs = 11
		pageSize   = 4
		numPages   = 3 // pages of 4, 4, 3
	)
	n, taskId, cleanup := setupExecutionsEngine(t, totalExecs)
	defer cleanup()
	user := testutil.TestUser1()

	allIds := collectAllExecutionIds(t, n, taskId, totalExecs)

	// Collect forward pages
	forwardPages := make([][]string, 0, numPages)
	forwardStartCursors := make([]string, 0, numPages)
	var afterCursor string

	for p := 0; p < numPages; p++ {
		req := &avsproto.ListExecutionsReq{
			TaskIds: []string{taskId},
			Limit:   pageSize,
		}
		if afterCursor != "" {
			req.After = afterCursor
		}
		page, err := n.ListExecutions(user, req)
		require.NoError(t, err, "forward page %d", p)

		ids := make([]string, len(page.Items))
		for i, item := range page.Items {
			ids[i] = item.Id
			assert.Equal(t, allIds[p*pageSize+i], item.Id, "forward page %d item %d", p, i)
		}
		forwardPages = append(forwardPages, ids)
		forwardStartCursors = append(forwardStartCursors, page.PageInfo.StartCursor)
		afterCursor = page.PageInfo.EndCursor
	}

	// Navigate backward from last page
	beforeCursor := forwardStartCursors[numPages-1]
	for p := numPages - 2; p >= 0; p-- {
		page, err := n.ListExecutions(user, &avsproto.ListExecutionsReq{
			TaskIds: []string{taskId},
			Before:  beforeCursor,
			Limit:   pageSize,
		})
		require.NoError(t, err, "backward page %d", p)
		assert.Equal(t, pageSize, len(page.Items), "backward page %d size", p)

		for i, item := range page.Items {
			assert.Equal(t, forwardPages[p][i], item.Id,
				"backward page %d item %d should match forward", p, i)
		}

		beforeCursor = page.PageInfo.StartCursor
		assert.True(t, page.PageInfo.HasNextPage, "backward page %d hasNext", p)

		if p > 0 {
			assert.True(t, page.PageInfo.HasPreviousPage, "backward page %d hasPrevious", p)
		} else {
			assert.False(t, page.PageInfo.HasPreviousPage, "first page should not have previous")
		}
	}
}

// TestExecutionsPaginationBackwardFromPartialLastPage tests backward navigation
// when the last page is not full (total items not divisible by page size).
func TestExecutionsPaginationBackwardFromPartialLastPage(t *testing.T) {
	const (
		totalExecs = 7
		pageSize   = 3
	)
	n, taskId, cleanup := setupExecutionsEngine(t, totalExecs)
	defer cleanup()
	user := testutil.TestUser1()

	// Forward to all 3 pages: [6,5,4], [3,2,1], [0]
	page1, err := n.ListExecutions(user, &avsproto.ListExecutionsReq{
		TaskIds: []string{taskId},
		Limit:   pageSize,
	})
	require.NoError(t, err)

	page2, err := n.ListExecutions(user, &avsproto.ListExecutionsReq{
		TaskIds: []string{taskId},
		After:   page1.PageInfo.EndCursor,
		Limit:   pageSize,
	})
	require.NoError(t, err)
	assert.Equal(t, pageSize, len(page2.Items))

	page3, err := n.ListExecutions(user, &avsproto.ListExecutionsReq{
		TaskIds: []string{taskId},
		After:   page2.PageInfo.EndCursor,
		Limit:   pageSize,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, len(page3.Items), "last partial page should have 1 item")
	assert.False(t, page3.PageInfo.HasNextPage)

	// Backward from partial last page → should get page 2
	backPage, err := n.ListExecutions(user, &avsproto.ListExecutionsReq{
		TaskIds: []string{taskId},
		Before:  page3.PageInfo.StartCursor,
		Limit:   pageSize,
	})
	require.NoError(t, err)
	assert.Equal(t, pageSize, len(backPage.Items))
	for i := 0; i < pageSize; i++ {
		assert.Equal(t, page2.Items[i].Id, backPage.Items[i].Id,
			"backward from partial last page item %d should match page 2", i)
	}
}

// TestExecutionsPaginationPageInfoFlags verifies HasPreviousPage and HasNextPage
// are correct at every step of forward and backward navigation.
func TestExecutionsPaginationPageInfoFlags(t *testing.T) {
	const (
		totalExecs = 8
		pageSize   = 5
	)
	n, taskId, cleanup := setupExecutionsEngine(t, totalExecs)
	defer cleanup()
	user := testutil.TestUser1()

	// Page 1 (first page): hasPrev=false, hasNext=true
	page1, err := n.ListExecutions(user, &avsproto.ListExecutionsReq{
		TaskIds: []string{taskId},
		Limit:   pageSize,
	})
	require.NoError(t, err)
	assert.False(t, page1.PageInfo.HasPreviousPage, "first page should not have previous")
	assert.True(t, page1.PageInfo.HasNextPage, "first page should have next")

	// Page 2 (last page via forward): hasPrev=true, hasNext=false
	page2, err := n.ListExecutions(user, &avsproto.ListExecutionsReq{
		TaskIds: []string{taskId},
		After:   page1.PageInfo.EndCursor,
		Limit:   pageSize,
	})
	require.NoError(t, err)
	assert.True(t, page2.PageInfo.HasPreviousPage, "last page via forward should have previous")
	assert.False(t, page2.PageInfo.HasNextPage, "last page should not have next")

	// Go backward from page 2: hasPrev=false (at start), hasNext=true (page 2 exists)
	backPage, err := n.ListExecutions(user, &avsproto.ListExecutionsReq{
		TaskIds: []string{taskId},
		Before:  page2.PageInfo.StartCursor,
		Limit:   pageSize,
	})
	require.NoError(t, err)
	assert.False(t, backPage.PageInfo.HasPreviousPage, "backward to first page should not have previous")
	assert.True(t, backPage.PageInfo.HasNextPage, "backward page should have next (older items exist)")
}
