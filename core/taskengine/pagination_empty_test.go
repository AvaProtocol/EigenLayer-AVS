package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func TestPaginationEmptyParameters(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	user := testutil.TestUser1()

	_, err := n.ListSecrets(user, &avsproto.ListSecretsReq{
		Before: "",
		After:  "some_cursor",
		Limit:  5,
	})
	if err == nil {
		t.Errorf("Expected error when before parameter is empty string, got nil")
	}

	_, err = n.ListSecrets(user, &avsproto.ListSecretsReq{
		Before: "some_cursor",
		After:  "",
		Limit:  5,
	})
	if err == nil {
		t.Errorf("Expected error when after parameter is empty string, got nil")
	}

	result, err := n.ListSecrets(user, &avsproto.ListSecretsReq{
		Before: "",
		After:  "",
		Limit:  5,
	})
	if err != nil {
		t.Errorf("Expected no error when both before and after are empty (first page), got %v", err)
	}
	if result == nil {
		t.Errorf("Expected valid result for first page")
	}

	_, err = n.ListTasksByUser(user, &avsproto.ListTasksReq{
		SmartWalletAddress: []string{"0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"},
		Before:             "",
		After:              "some_cursor",
		Limit:              5,
	})
	if err == nil {
		t.Errorf("Expected error when before parameter is empty string for ListTasksByUser, got nil")
	}

	tr := testutil.JsFastTask()
	tr.SmartWalletAddress = "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"
	task, _ := n.CreateTask(user, tr)

	_, err = n.ListExecutions(user, &avsproto.ListExecutionsReq{
		TaskIds: []string{task.Id},
		Before:  "",
		After:   "some_cursor",
		Limit:   5,
	})
	if err == nil {
		t.Errorf("Expected error when before parameter is empty string for ListExecutions, got nil")
	}
}
