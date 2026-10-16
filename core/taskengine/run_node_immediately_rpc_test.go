package taskengine

import (
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"testing"
)

// Read/data nodes remain supported by the standalone RPC.
func TestRunNodeImmediatelyRPC(t *testing.T) {
	t.Run("BalanceNode_ConfigViaInputVariables", func(t *testing.T) {
		// Skip if Moralis API key is not configured
		moralisAPIKey := testutil.GetTestMoralisApiKey()
		if moralisAPIKey == "" {
			t.Skip("Moralis API key not configured, skipping BalanceNode test")
		}

		// No need to manually call SetMacroSecrets() - Engine.New() does this automatically

		// Setup test environment
		db := testutil.TestMustDB()
		defer storage.Destroy(db.(*storage.BadgerStorage))

		config := testutil.GetAggregatorConfig()
		engine := New(db, config, nil, testutil.GetLogger())

		// Create test user
		ownerAddr, ok := testutil.MustGetTestOwnerAddress()
		if !ok {
			t.Skip("Owner EOA address not set, skipping RPC test")
		}
		ownerEOA := *ownerAddr
		user := &model.User{Address: ownerEOA}

		// Create BalanceNode with full config
		balanceNode := &avsproto.TaskNode{
			Id:   "test-balance",
			Name: "checkBalance",
			Type: avsproto.NodeType_NODE_TYPE_BALANCE,
			TaskType: &avsproto.TaskNode_Balance{
				Balance: &avsproto.BalanceNode{
					Config: &avsproto.BalanceNode_Config{
						Address:             "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f",
						Chain:               "sepolia",
						IncludeSpam:         false,
						IncludeZeroBalances: false,
						TokenAddresses:      []string{"0x1c7d4b196cb0c7b01d743fbc6116a902379c7238"},
					},
				},
			},
		}

		// Create protobuf request with the full TaskNode
		req := &avsproto.RunNodeWithInputsReq{
			Node: balanceNode,
		}

		// Settings
		settingsData := map[string]interface{}{
			"chain_id": 11155111,
		}

		// Build inputVariables
		req.InputVariables = make(map[string]*structpb.Value)

		settingsVal, err := structpb.NewValue(settingsData)
		require.NoError(t, err)
		req.InputVariables["settings"] = settingsVal

		t.Logf("🧪 Testing BalanceNode via RPC with full TaskNode")

		// Execute
		result, err := engine.RunNodeImmediatelyRPC(user, req)

		// Assertions
		require.NoError(t, err, "RunNodeImmediatelyRPC should succeed for balance node")
		require.NotNil(t, result, "Should get response")

		// Log error if success is false
		if !result.Success {
			t.Logf("❌ Balance check failed with error: %s", result.Error)
		}

		assert.True(t, result.Success, "Balance check should succeed")

		// Verify we got balance data
		if result.GetBalance() != nil {
			balanceOutput := result.GetBalance()
			assert.NotNil(t, balanceOutput.Data, "Should have balance data")
			t.Logf("✅ Got balance data")
		}

		t.Logf("✅ BalanceNode RPC test completed successfully")
	})

}
