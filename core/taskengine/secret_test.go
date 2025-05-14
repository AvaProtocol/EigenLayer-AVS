package taskengine

import (
	"reflect"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func TestLoadSecretForTask(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())

	user1 := testutil.TestUser1()
	n.CreateSecret(user1, &avsproto.CreateOrUpdateSecretReq{
		Name:   "secret1",
		Secret: "mykey1",
	})

	n.CreateSecret(user1, &avsproto.CreateOrUpdateSecretReq{
		Name:       "secret1",
		Secret:     "secretworkflow123",
		WorkflowId: "workflow123",
	})

	n.CreateSecret(user1, &avsproto.CreateOrUpdateSecretReq{
		Name:       "secret2",
		Secret:     "mykey2",
		WorkflowId: "workflow123",
	})

	n.CreateSecret(user1, &avsproto.CreateOrUpdateSecretReq{
		Name:       "secret3",
		Secret:     "mykey2",
		WorkflowId: "worklow456",
	})

	secrets, err := LoadSecretForTask(db, &model.Task{
		Task: &avsproto.Task{
			Owner: user1.Address.Hex(),
			Id:    "workflow123",
		},
	})

	if err != nil {
		t.Errorf("expect no error fetching secret but got error: %s", err)
	}

	if !reflect.DeepEqual(map[string]string{
		"secret1": "mykey1",
		"secret2": "mykey2",
	}, secrets) {
		t.Errorf("expect found secrets map[secret1:mykey1 secret2:mykey2] but got %v", secrets)
	}
}
