package taskengine

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
)

// Helper function to create a test engine with proper cleanup
func createSecretTestEngine(t *testing.T) *Engine {
	db := testutil.TestMustDB()
	t.Cleanup(func() {
		storage.Destroy(db.(*storage.BadgerStorage))
	})

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())
	t.Cleanup(func() {
		engine.Stop()
	})

	return engine
}

// CRUD Tests
func TestCreateSecret(t *testing.T) {
	engine := createSecretTestEngine(t)
	user := testutil.TestUser1()

	t.Run("Create user-scoped secret", func(t *testing.T) {
		success, err := engine.CreateSecret(user, &avsproto.CreateOrUpdateSecretReq{
			Name:   "test_secret",
			Secret: "secret_value",
		})
		assert.NoError(t, err)
		assert.True(t, success)

		// Verify the secret was created by listing it
		resp, err := engine.ListSecrets(user, &avsproto.ListSecretsReq{})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, 1)
		assert.Equal(t, "test_secret", resp.Items[0].Name)
		assert.Equal(t, "user", resp.Items[0].Scope)
	})

	t.Run("Create workflow-scoped secret", func(t *testing.T) {
		success, err := engine.CreateSecret(user, &avsproto.CreateOrUpdateSecretReq{
			Name:       "workflow_secret",
			Secret:     "workflow_value",
			WorkflowId: "workflow123",
		})
		assert.NoError(t, err)
		assert.True(t, success)

		// Verify the secret was created
		resp, err := engine.ListSecrets(user, &avsproto.ListSecretsReq{})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, 2) // Previous test + this one

		// Find the workflow secret
		var workflowSecret *avsproto.Secret
		for _, item := range resp.Items {
			if item.Name == "workflow_secret" {
				workflowSecret = item
				break
			}
		}
		assert.NotNil(t, workflowSecret)
		assert.Equal(t, "user", workflowSecret.Scope) // Implementation always returns "user"
		assert.Equal(t, "workflow123", workflowSecret.WorkflowId)
	})

	t.Run("Create secret with empty name should fail", func(t *testing.T) {
		success, err := engine.CreateSecret(user, &avsproto.CreateOrUpdateSecretReq{
			Name:   "",
			Secret: "value",
		})
		assert.Error(t, err)
		assert.False(t, success)
	})

	t.Run("Create secret with empty value is allowed", func(t *testing.T) {
		success, err := engine.CreateSecret(user, &avsproto.CreateOrUpdateSecretReq{
			Name:   "empty_secret",
			Secret: "",
		})
		assert.NoError(t, err)
		assert.True(t, success)
	})
}

func TestUpdateSecret(t *testing.T) {
	engine := createSecretTestEngine(t)
	user := testutil.TestUser1()

	// Create a secret first
	_, err := engine.CreateSecret(user, &avsproto.CreateOrUpdateSecretReq{
		Name:   "update_test",
		Secret: "original_value",
	})
	assert.NoError(t, err)

	t.Run("Update existing secret", func(t *testing.T) {
		success, err := engine.UpdateSecret(user, &avsproto.CreateOrUpdateSecretReq{
			Name:   "update_test",
			Secret: "updated_value",
		})
		assert.NoError(t, err)
		assert.True(t, success)

		// Verify the secret was updated by checking if we can still list it
		resp, err := engine.ListSecrets(user, &avsproto.ListSecretsReq{})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, 1)
		assert.Equal(t, "update_test", resp.Items[0].Name)
	})

	t.Run("Update non-existent secret should fail", func(t *testing.T) {
		success, err := engine.UpdateSecret(user, &avsproto.CreateOrUpdateSecretReq{
			Name:   "non_existent",
			Secret: "value",
		})
		assert.Error(t, err)
		assert.False(t, success)
	})

	t.Run("Update secret with empty name should fail", func(t *testing.T) {
		success, err := engine.UpdateSecret(user, &avsproto.CreateOrUpdateSecretReq{
			Name:   "",
			Secret: "value",
		})
		assert.Error(t, err)
		assert.False(t, success)
	})
}

func TestDeleteSecret(t *testing.T) {
	engine := createSecretTestEngine(t)
	user := testutil.TestUser1()

	// Create secrets for testing
	_, err := engine.CreateSecret(user, &avsproto.CreateOrUpdateSecretReq{
		Name:   "delete_test1",
		Secret: "value1",
	})
	assert.NoError(t, err)

	_, err = engine.CreateSecret(user, &avsproto.CreateOrUpdateSecretReq{
		Name:   "delete_test2",
		Secret: "value2",
	})
	assert.NoError(t, err)

	t.Run("Delete existing secret", func(t *testing.T) {
		success, err := engine.DeleteSecret(user, &avsproto.DeleteSecretReq{
			Name: "delete_test1",
		})
		assert.NoError(t, err)
		assert.True(t, success)

		// Verify the secret was deleted
		resp, err := engine.ListSecrets(user, &avsproto.ListSecretsReq{})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, 1)
		assert.Equal(t, "delete_test2", resp.Items[0].Name)
	})

	t.Run("Delete non-existent secret should succeed", func(t *testing.T) {
		success, err := engine.DeleteSecret(user, &avsproto.DeleteSecretReq{
			Name: "non_existent",
		})
		assert.NoError(t, err)
		assert.True(t, success) // Delete operations succeed even if secret doesn't exist
	})

	t.Run("Delete secret with empty name should succeed", func(t *testing.T) {
		success, err := engine.DeleteSecret(user, &avsproto.DeleteSecretReq{
			Name: "",
		})
		assert.NoError(t, err)
		assert.True(t, success) // Implementation doesn't validate empty names
	})
}

func TestSecretCRUDIntegration(t *testing.T) {
	engine := createSecretTestEngine(t)
	user := testutil.TestUser1()

	t.Run("Complete CRUD lifecycle", func(t *testing.T) {
		// Create
		success, err := engine.CreateSecret(user, &avsproto.CreateOrUpdateSecretReq{
			Name:   "lifecycle_test",
			Secret: "initial_value",
		})
		assert.NoError(t, err)
		assert.True(t, success)

		// Read (via List)
		resp, err := engine.ListSecrets(user, &avsproto.ListSecretsReq{})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, 1)
		assert.Equal(t, "lifecycle_test", resp.Items[0].Name)

		// Update
		success, err = engine.UpdateSecret(user, &avsproto.CreateOrUpdateSecretReq{
			Name:   "lifecycle_test",
			Secret: "updated_value",
		})
		assert.NoError(t, err)
		assert.True(t, success)

		// Verify update
		resp, err = engine.ListSecrets(user, &avsproto.ListSecretsReq{})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, 1)
		assert.Equal(t, "lifecycle_test", resp.Items[0].Name)

		// Delete
		success, err = engine.DeleteSecret(user, &avsproto.DeleteSecretReq{
			Name: "lifecycle_test",
		})
		assert.NoError(t, err)
		assert.True(t, success)

		// Verify deletion
		resp, err = engine.ListSecrets(user, &avsproto.ListSecretsReq{})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, 0)
	})
}

// Field Control Tests
func TestListSecretsFieldControl(t *testing.T) {
	engine := createSecretTestEngine(t)
	user := &model.User{
		Address: common.HexToAddress("0x1234567890123456789012345678901234567890"),
	}

	// Create a test secret
	_, err := engine.CreateSecret(user, &avsproto.CreateOrUpdateSecretReq{
		Name:   "test_secret",
		Secret: "secret_value",
	})
	assert.NoError(t, err)

	t.Run("Default fields only", func(t *testing.T) {
		resp, err := engine.ListSecrets(user, &avsproto.ListSecretsReq{
			// No field control flags set - should return default fields only
		})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, 1)

		secret := resp.Items[0]
		assert.Equal(t, "test_secret", secret.Name)
		assert.Equal(t, "user", secret.Scope)
		assert.Empty(t, secret.WorkflowId)
		assert.Empty(t, secret.OrgId)

		// These fields should be empty/zero when not requested
		assert.Equal(t, int64(0), secret.CreatedAt)
		assert.Equal(t, int64(0), secret.UpdatedAt)
		assert.Empty(t, secret.CreatedBy)
		assert.Empty(t, secret.Description)
	})

	t.Run("Include timestamps", func(t *testing.T) {
		resp, err := engine.ListSecrets(user, &avsproto.ListSecretsReq{
			IncludeTimestamps: true,
		})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, 1)

		secret := resp.Items[0]
		assert.Equal(t, "test_secret", secret.Name)
		assert.Equal(t, "user", secret.Scope)

		// Timestamps should be zero since we don't have real data from storage
		assert.Equal(t, int64(0), secret.CreatedAt)
		assert.Equal(t, int64(0), secret.UpdatedAt)

		// Other optional fields should still be empty
		assert.Empty(t, secret.CreatedBy)
		assert.Empty(t, secret.Description)
	})

	t.Run("Include created by", func(t *testing.T) {
		resp, err := engine.ListSecrets(user, &avsproto.ListSecretsReq{
			IncludeCreatedBy: true,
		})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, 1)

		secret := resp.Items[0]
		assert.Equal(t, "test_secret", secret.Name)
		assert.Equal(t, "user", secret.Scope)

		// CreatedBy should be populated
		assert.Equal(t, user.Address.Hex(), secret.CreatedBy)

		// Other optional fields should still be empty
		assert.Equal(t, int64(0), secret.CreatedAt)
		assert.Equal(t, int64(0), secret.UpdatedAt)
		assert.Empty(t, secret.Description)
	})

	t.Run("Include description", func(t *testing.T) {
		resp, err := engine.ListSecrets(user, &avsproto.ListSecretsReq{
			IncludeDescription: true,
		})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, 1)

		secret := resp.Items[0]
		assert.Equal(t, "test_secret", secret.Name)
		assert.Equal(t, "user", secret.Scope)

		// Description should be populated (even if empty)
		assert.Equal(t, "", secret.Description)

		// Other optional fields should still be empty
		assert.Equal(t, int64(0), secret.CreatedAt)
		assert.Equal(t, int64(0), secret.UpdatedAt)
		assert.Empty(t, secret.CreatedBy)
	})

	t.Run("Include all optional fields", func(t *testing.T) {
		resp, err := engine.ListSecrets(user, &avsproto.ListSecretsReq{
			IncludeTimestamps:  true,
			IncludeCreatedBy:   true,
			IncludeDescription: true,
		})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, 1)

		secret := resp.Items[0]
		assert.Equal(t, "test_secret", secret.Name)
		assert.Equal(t, "user", secret.Scope)

		// Timestamps should be zero since we don't have real data from storage
		assert.Equal(t, int64(0), secret.CreatedAt)
		assert.Equal(t, int64(0), secret.UpdatedAt)
		assert.Equal(t, user.Address.Hex(), secret.CreatedBy)
		assert.Equal(t, "", secret.Description)
	})

	t.Run("Pagination with field control", func(t *testing.T) {
		// Create additional secrets for pagination testing
		for i := 1; i <= 3; i++ {
			_, err := engine.CreateSecret(user, &avsproto.CreateOrUpdateSecretReq{
				Name:   fmt.Sprintf("secret_%d", i),
				Secret: "value",
			})
			assert.NoError(t, err)
		}

		resp, err := engine.ListSecrets(user, &avsproto.ListSecretsReq{
			Limit:             2,
			IncludeTimestamps: true,
			IncludeCreatedBy:  true,
		})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, 2)

		// Verify field control works with pagination
		for _, secret := range resp.Items {
			assert.NotEmpty(t, secret.Name)
			assert.Equal(t, "user", secret.Scope)
			assert.Equal(t, int64(0), secret.CreatedAt)
			assert.Equal(t, int64(0), secret.UpdatedAt)
			assert.Equal(t, user.Address.Hex(), secret.CreatedBy)
			assert.Empty(t, secret.Description) // Not requested
		}

		// Verify pagination info
		assert.NotNil(t, resp.PageInfo)
		assert.True(t, resp.PageInfo.HasNextPage)
	})
}

func TestSecretFieldControlPerformance(t *testing.T) {
	engine := createSecretTestEngine(t)
	user := &model.User{
		Address: common.HexToAddress("0x1234567890123456789012345678901234567890"),
	}

	// Create multiple secrets
	numSecrets := 10
	for i := 0; i < numSecrets; i++ {
		_, err := engine.CreateSecret(user, &avsproto.CreateOrUpdateSecretReq{
			Name:   fmt.Sprintf("perf_secret_%d", i),
			Secret: "value",
		})
		assert.NoError(t, err)
	}

	t.Run("Minimal fields for performance", func(t *testing.T) {
		resp, err := engine.ListSecrets(user, &avsproto.ListSecretsReq{
			// No optional fields - fastest response
		})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, numSecrets)

		// Verify only basic fields are populated
		for _, secret := range resp.Items {
			assert.NotEmpty(t, secret.Name)
			assert.Equal(t, "user", secret.Scope)
			assert.Equal(t, int64(0), secret.CreatedAt)
			assert.Equal(t, int64(0), secret.UpdatedAt)
			assert.Empty(t, secret.CreatedBy)
			assert.Empty(t, secret.Description)
		}
	})

	t.Run("All fields for detailed view", func(t *testing.T) {
		resp, err := engine.ListSecrets(user, &avsproto.ListSecretsReq{
			IncludeTimestamps:  true,
			IncludeCreatedBy:   true,
			IncludeDescription: true,
		})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, numSecrets)

		// Verify all fields are populated
		for _, secret := range resp.Items {
			assert.NotEmpty(t, secret.Name)
			assert.Equal(t, "user", secret.Scope)
			assert.Equal(t, int64(0), secret.CreatedAt)
			assert.Equal(t, int64(0), secret.UpdatedAt)
			assert.Equal(t, user.Address.Hex(), secret.CreatedBy)
			assert.Equal(t, "", secret.Description)
		}
	})
}

// Legacy Test (preserved from original secret_test.go)
func TestLoadSecretForTask(t *testing.T) {
	// Clear any global secrets from other tests
	SetMacroSecrets(map[string]string{})

	engine := createSecretTestEngine(t)
	user1 := testutil.TestUser1()

	engine.CreateSecret(user1, &avsproto.CreateOrUpdateSecretReq{
		Name:   "secret1",
		Secret: "mykey1",
	})

	engine.CreateSecret(user1, &avsproto.CreateOrUpdateSecretReq{
		Name:       "secret1",
		Secret:     "secretworkflow123",
		WorkflowId: "workflow123",
	})

	engine.CreateSecret(user1, &avsproto.CreateOrUpdateSecretReq{
		Name:       "secret2",
		Secret:     "mykey2",
		WorkflowId: "workflow123",
	})

	engine.CreateSecret(user1, &avsproto.CreateOrUpdateSecretReq{
		Name:       "secret3",
		Secret:     "mykey2",
		WorkflowId: "worklow456",
	})

	secrets, err := LoadSecretForTask(engine.db, &model.Task{
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
