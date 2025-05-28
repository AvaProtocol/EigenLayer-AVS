package taskengine

import (
	"fmt"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/assert"
)

func TestListSecretsFieldControl(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	defer n.Stop()

	user := &model.User{
		Address: common.HexToAddress("0x1234567890123456789012345678901234567890"),
	}

	// Create a test secret
	_, err := n.CreateSecret(user, &avsproto.CreateOrUpdateSecretReq{
		Name:   "test_secret",
		Secret: "secret_value",
	})
	assert.NoError(t, err)

	t.Run("Default fields only", func(t *testing.T) {
		resp, err := n.ListSecrets(user, &avsproto.ListSecretsReq{
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
		resp, err := n.ListSecrets(user, &avsproto.ListSecretsReq{
			IncludeTimestamps: true,
		})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, 1)

		secret := resp.Items[0]
		assert.Equal(t, "test_secret", secret.Name)
		assert.Equal(t, "user", secret.Scope)

		// Timestamps should be populated
		assert.NotEqual(t, int64(0), secret.CreatedAt)
		assert.NotEqual(t, int64(0), secret.UpdatedAt)

		// Other optional fields should still be empty
		assert.Empty(t, secret.CreatedBy)
		assert.Empty(t, secret.Description)
	})

	t.Run("Include created by", func(t *testing.T) {
		resp, err := n.ListSecrets(user, &avsproto.ListSecretsReq{
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
		resp, err := n.ListSecrets(user, &avsproto.ListSecretsReq{
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
		resp, err := n.ListSecrets(user, &avsproto.ListSecretsReq{
			IncludeTimestamps:  true,
			IncludeCreatedBy:   true,
			IncludeDescription: true,
		})
		assert.NoError(t, err)
		assert.Len(t, resp.Items, 1)

		secret := resp.Items[0]
		assert.Equal(t, "test_secret", secret.Name)
		assert.Equal(t, "user", secret.Scope)

		// All optional fields should be populated
		assert.NotEqual(t, int64(0), secret.CreatedAt)
		assert.NotEqual(t, int64(0), secret.UpdatedAt)
		assert.Equal(t, user.Address.Hex(), secret.CreatedBy)
		assert.Equal(t, "", secret.Description)
	})

	t.Run("Pagination with field control", func(t *testing.T) {
		// Create additional secrets for pagination testing
		for i := 1; i <= 3; i++ {
			_, err := n.CreateSecret(user, &avsproto.CreateOrUpdateSecretReq{
				Name:   fmt.Sprintf("secret_%d", i),
				Secret: "value",
			})
			assert.NoError(t, err)
		}

		resp, err := n.ListSecrets(user, &avsproto.ListSecretsReq{
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
			assert.NotEqual(t, int64(0), secret.CreatedAt)
			assert.NotEqual(t, int64(0), secret.UpdatedAt)
			assert.Equal(t, user.Address.Hex(), secret.CreatedBy)
			assert.Empty(t, secret.Description) // Not requested
		}

		// Verify pagination info
		assert.NotNil(t, resp.PageInfo)
		assert.True(t, resp.PageInfo.HasNextPage)
	})
}

func TestSecretFieldControlPerformance(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	n := New(db, config, nil, testutil.GetLogger())
	defer n.Stop()

	user := &model.User{
		Address: common.HexToAddress("0x1234567890123456789012345678901234567890"),
	}

	// Create multiple secrets
	numSecrets := 10
	for i := 0; i < numSecrets; i++ {
		_, err := n.CreateSecret(user, &avsproto.CreateOrUpdateSecretReq{
			Name:   fmt.Sprintf("perf_secret_%d", i),
			Secret: "value",
		})
		assert.NoError(t, err)
	}

	t.Run("Minimal fields for performance", func(t *testing.T) {
		resp, err := n.ListSecrets(user, &avsproto.ListSecretsReq{
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
		resp, err := n.ListSecrets(user, &avsproto.ListSecretsReq{
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
			assert.NotEqual(t, int64(0), secret.CreatedAt)
			assert.NotEqual(t, int64(0), secret.UpdatedAt)
			assert.Equal(t, user.Address.Hex(), secret.CreatedBy)
			assert.Equal(t, "", secret.Description)
		}
	})
}
