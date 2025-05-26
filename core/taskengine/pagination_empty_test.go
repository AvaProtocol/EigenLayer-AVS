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

	result, err = n.ListSecrets(user, &avsproto.ListSecretsReq{
		Before: "",
		After:  "eyJkIjoibmV4dCIsInAiOiIwIn0=", // valid cursor
		Limit:  5,
	})
	if err != nil {
		t.Errorf("Expected no error for normal forward pagination, got %v", err)
	}

	result, err = n.ListSecrets(user, &avsproto.ListSecretsReq{
		Before: "eyJkIjoibmV4dCIsInAiOiIwIn0=", // valid cursor
		After:  "",
		Limit:  5,
	})
	if err != nil {
		t.Errorf("Expected no error for normal backward pagination, got %v", err)
	}

	taskResult, err := n.ListTasksByUser(user, &avsproto.ListTasksReq{
		SmartWalletAddress: []string{"0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"},
		Before:             "",
		After:              "",
		Limit:              5,
	})
	if err != nil {
		t.Errorf("Expected no error for first page of ListTasksByUser, got %v", err)
	}
	if taskResult == nil {
		t.Errorf("Expected valid result for first page of ListTasksByUser")
	}

	tr := testutil.JsFastTask()
	tr.SmartWalletAddress = "0x7c3a76086588230c7B3f4839A4c1F5BBafcd57C6"
	task, _ := n.CreateTask(user, tr)

	execResult, err := n.ListExecutions(user, &avsproto.ListExecutionsReq{
		TaskIds: []string{task.Id},
		Before:  "",
		After:   "",
		Limit:   5,
	})
	if err != nil {
		t.Errorf("Expected no error for first page of ListExecutions, got %v", err)
	}
	if execResult == nil {
		t.Errorf("Expected valid result for first page of ListExecutions")
	}
}
