package taskengine

import (
	"fmt"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/stretchr/testify/assert"
)

func TestListTasksByUserPaginationWithBeforeAfter(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	
	user := testutil.TestUser1()
	
	wallet, err := n.GetWallet(user, &avsproto.GetWalletReq{
		Salt: "12345",
	})
	if err != nil {
		t.Fatalf("Failed to get wallet: %v", err)
	}
	
	const (
		totalTestTasks = 10
		pageSize       = 3
	)
	
	for i := 0; i < totalTestTasks; i++ {
		task := testutil.RestTask()
		task.Name = fmt.Sprintf("task%d", i)
		task.SmartWalletAddress = wallet.Address
		_, err := n.CreateTask(user, task)
		if err != nil {
			t.Fatalf("Failed to create task: %v", err)
		}
	}
	
	result, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
		SmartWalletAddress: []string{wallet.Address},
		Limit:              pageSize,
	})
	if err != nil {
		t.Errorf("ListTasksByUser failed: %v", err)
		return
	}
	
	assert.Equal(t, pageSize, len(result.Items), "Expected %d items with limit %d, got %d", pageSize, pageSize, len(result.Items))
	assert.True(t, result.HasMore, "Expected HasMore to be true with limit %d and %d total items", pageSize, totalTestTasks)
	assert.NotEmpty(t, result.Cursor, "Expected cursor to be set when HasMore is true")
	
	firstPage := result
	secondPage, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
		SmartWalletAddress: []string{wallet.Address},
		After:              firstPage.Cursor,
		Limit:              pageSize,
	})
	if err != nil {
		t.Errorf("ListTasksByUser with 'after' parameter failed: %v", err)
		return
	}
	
	assert.Equal(t, pageSize, len(secondPage.Items), "Expected %d items in second page", pageSize)
	assert.True(t, secondPage.HasMore, "Expected HasMore to be true for second page")
	
	firstPageIds := make(map[string]bool)
	for _, item := range firstPage.Items {
		firstPageIds[item.Id] = true
	}
	
	for _, item := range secondPage.Items {
		assert.False(t, firstPageIds[item.Id], "Found duplicate task ID %s in second page", item.Id)
	}
	
	previousPage, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
		SmartWalletAddress: []string{wallet.Address},
		Before:             secondPage.Cursor,
		Limit:              pageSize,
	})
	if err != nil {
		t.Errorf("ListTasksByUser with 'before' parameter failed: %v", err)
		return
	}
	
	assert.Equal(t, pageSize, len(previousPage.Items), "Expected %d items in previous page", pageSize)
	
	
	assert.Equal(t, pageSize, len(previousPage.Items), "Expected %d items in previous page", pageSize)
	
	secondPageIds := make(map[string]bool)
	for _, item := range secondPage.Items {
		secondPageIds[item.Id] = true
	}
	
	for _, item := range previousPage.Items {
		assert.False(t, secondPageIds[item.Id], "Found duplicate task ID %s in previous and second page", item.Id)
	}
	
	defaultLimitResult, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
		SmartWalletAddress: []string{wallet.Address},
		Limit:              0,
	})
	if err != nil {
		t.Errorf("ListTasksByUser with default limit failed: %v", err)
		return
	}
	
	assert.Equal(t, totalTestTasks, len(defaultLimitResult.Items), "Expected %d items with default limit of 0", totalTestTasks)
	
	largeLimit := totalTestTasks * 2
	largeLimitResult, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
		SmartWalletAddress: []string{wallet.Address},
		Limit:              int64(largeLimit),
	})
	if err != nil {
		t.Errorf("ListTasksByUser with large limit failed: %v", err)
		return
	}
	
	assert.Equal(t, totalTestTasks, len(largeLimitResult.Items), "Expected %d items with limit %d", totalTestTasks, largeLimit)
	assert.False(t, largeLimitResult.HasMore, "Expected HasMore to be false when limit exceeds total items")
	assert.Empty(t, largeLimitResult.Cursor, "Expected cursor to be empty when HasMore is false")
}
