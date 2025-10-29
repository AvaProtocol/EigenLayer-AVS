package taskengine

import (
	"math/big"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// TestRunNodeImmediatelyRPC verifies the RPC layer correctly extracts node config from inputVariables["config"]
func TestRunNodeImmediatelyRPC(t *testing.T) {
	t.Run("ContractWrite_ConfigViaInputVariables", func(t *testing.T) {
		// Setup test environment
		db := testutil.TestMustDB()
		defer storage.Destroy(db.(*storage.BadgerStorage))

		config := testutil.GetAggregatorConfig()
		engine := New(db, config, nil, testutil.GetLogger())

		smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
		aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

		// Create test user (simulating authenticated user from JWT)
		ownerAddr, ok := testutil.MustGetTestOwnerAddress()
		if !ok {
			t.Skip("Owner EOA address not set, skipping RPC test")
		}
		ownerEOA := *ownerAddr
		factory := testutil.GetAggregatorConfig().SmartWallet.FactoryAddress

		// Connect to RPC client for GetSenderAddress
		client, err := ethclient.Dial(config.SmartWallet.EthRpcUrl)
		require.NoError(t, err, "Failed to connect to RPC")
		defer client.Close()

		// Derive actual salt:0 smart wallet address
		aa.SetFactoryAddress(factory)
		runnerAddr, err := aa.GetSenderAddress(client, ownerEOA, big.NewInt(0))
		require.NoError(t, err, "Failed to derive smart wallet address")

		// Create authenticated user model
		user := &model.User{
			Address: ownerEOA,
		}

		// Seed wallet in DB for validation
		_ = StoreWallet(db, ownerEOA, &model.SmartWallet{
			Owner:   &ownerEOA,
			Address: runnerAddr,
			Factory: &factory,
			Salt:    big.NewInt(0),
		})

		// Create ContractWrite node with full config
		contractAbi := []*structpb.Value{
			structpb.NewStructValue(&structpb.Struct{
				Fields: map[string]*structpb.Value{
					"inputs": structpb.NewListValue(&structpb.ListValue{
						Values: []*structpb.Value{
							structpb.NewStructValue(&structpb.Struct{
								Fields: map[string]*structpb.Value{
									"internalType": structpb.NewStringValue("address"),
									"name":         structpb.NewStringValue("spender"),
									"type":         structpb.NewStringValue("address"),
								},
							}),
							structpb.NewStructValue(&structpb.Struct{
								Fields: map[string]*structpb.Value{
									"internalType": structpb.NewStringValue("uint256"),
									"name":         structpb.NewStringValue("amount"),
									"type":         structpb.NewStringValue("uint256"),
								},
							}),
						},
					}),
					"name": structpb.NewStringValue("approve"),
					"outputs": structpb.NewListValue(&structpb.ListValue{
						Values: []*structpb.Value{
							structpb.NewStructValue(&structpb.Struct{
								Fields: map[string]*structpb.Value{
									"internalType": structpb.NewStringValue("bool"),
									"name":         structpb.NewStringValue(""),
									"type":         structpb.NewStringValue("bool"),
								},
							}),
						},
					}),
					"stateMutability": structpb.NewStringValue("nonpayable"),
					"type":            structpb.NewStringValue("function"),
				},
			}),
		}

		value := "0"
		gasLimit := "210000"
		isSimulated := true

		contractWriteNode := &avsproto.TaskNode{
			Id:   "test-contract-write",
			Name: "approve",
			Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
			TaskType: &avsproto.TaskNode_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode{
					Config: &avsproto.ContractWriteNode_Config{
						ContractAddress: "0xA0b86a33E6441d0be3c7bb50e65Eb42d5E0b2b4b",
						ContractAbi:     contractAbi,
						MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
							{
								MethodName:   "approve",
								MethodParams: []string{"0x1234567890123456789012345678901234567890", "1000000"},
							},
						},
						IsSimulated: &isSimulated,
						Value:       &value,
						GasLimit:    &gasLimit,
					},
				},
			},
		}

		// Create protobuf request with the full TaskNode
		req := &avsproto.RunNodeWithInputsReq{
			Node: contractWriteNode,
		}

		// Settings for the workflow
		settingsData := map[string]interface{}{
			"runner":   runnerAddr.Hex(),
			"chain_id": 11155111, // Sepolia testnet
		}

		// Build inputVariables map with settings
		req.InputVariables = make(map[string]*structpb.Value)

		// Add settings
		settingsVal, err := structpb.NewValue(settingsData)
		require.NoError(t, err)
		req.InputVariables["settings"] = settingsVal

		t.Logf("🧪 Testing RunNodeImmediatelyRPC with full TaskNode:")
		t.Logf("   User (from JWT): %s", user.Address.Hex())
		t.Logf("   Runner (from settings): %s", runnerAddr.Hex())
		t.Logf("   Config passed through: TaskNode.ContractWrite.Config")
		t.Logf("   Method: approve")

		// Execute via RPC layer
		result, err := engine.RunNodeImmediatelyRPC(user, req)

		// Assertions
		require.NoError(t, err, "RunNodeImmediatelyRPC should succeed")
		require.NotNil(t, result, "Should get response")
		assert.True(t, result.Success, "Contract write should succeed in simulation")

		// Verify execution context
		if result.ExecutionContext != nil {
			ctx := result.ExecutionContext.AsInterface()
			if ctxMap, ok := ctx.(map[string]interface{}); ok {
				assert.Equal(t, true, ctxMap["is_simulated"], "Should be simulated")
				assert.Equal(t, "tenderly", ctxMap["provider"], "Should use Tenderly for simulation")
				t.Logf("✅ ExecutionContext correct: provider=%s, is_simulated=%v",
					ctxMap["provider"], ctxMap["is_simulated"])
			}
		}

		t.Logf("✅ RunNodeImmediatelyRPC with full TaskNode completed successfully")
	})

	t.Run("BalanceNode_ConfigViaInputVariables", func(t *testing.T) {
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
