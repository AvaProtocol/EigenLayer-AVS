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

// TestRunNodeWithInputsRespectsIsSimulatedFlag verifies that the runNodeWithInputs RPC
// correctly respects the isSimulated flag from the node configuration.
//
// This test addresses the bug where the response executionContext showed isSimulated: true
// even when the request specified isSimulated: false in the node config.
//
// The root cause was that RunNodeImmediatelyRPC didn't pass the isSimulated value from
// nodeConfig to RunNodeImmediately(), causing it to default to simulation mode (true).
func TestRunNodeWithInputsRespectsIsSimulatedFlag(t *testing.T) {
	tests := []struct {
		name                    string
		isSimulated             bool
		expectedIsSimulated     bool
		expectedProviderPattern string // Provider can vary based on execution path
		description             string
	}{
		{
			name:                    "isSimulated: false should execute in real mode",
			isSimulated:             false,
			expectedIsSimulated:     false,
			expectedProviderPattern: "bundler", // Real execution uses bundler
			description:             "When isSimulated is false, executionContext should show isSimulated: false",
		},
		{
			name:                    "isSimulated: true should execute in simulation mode",
			isSimulated:             true,
			expectedIsSimulated:     true,
			expectedProviderPattern: "tenderly", // Simulation uses Tenderly
			description:             "When isSimulated is true, executionContext should show isSimulated: true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
				t.Skip("Owner EOA address not set, skipping test")
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

			// Create ContractWrite node with the isSimulated flag set according to the test case
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

			// This is the key part: set isSimulated according to test case
			isSimulated := tt.isSimulated

			contractWriteNode := &avsproto.TaskNode{
				Id:   "test-contract-write-simulation",
				Name: "approve",
				Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
				TaskType: &avsproto.TaskNode_ContractWrite{
					ContractWrite: &avsproto.ContractWriteNode{
						Config: &avsproto.ContractWriteNode_Config{
							ContractAddress: "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238", // USDC on Sepolia
							ContractAbi:     contractAbi,
							MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
								{
									MethodName:   "approve",
									MethodParams: []string{"0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E", "1000000"},
								},
							},
							IsSimulated: &isSimulated, // Set from test case
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

			t.Logf("🧪 Testing RunNodeWithInputs with isSimulated=%v", tt.isSimulated)
			t.Logf("   User (from JWT): %s", user.Address.Hex())
			t.Logf("   Runner (from settings): %s", runnerAddr.Hex())
			t.Logf("   Expected executionContext.isSimulated: %v", tt.expectedIsSimulated)
			t.Logf("   Expected provider pattern: %s", tt.expectedProviderPattern)

			// Execute via RPC layer
			result, err := engine.RunNodeImmediatelyRPC(user, req)

			// Assertions
			require.NoError(t, err, "RunNodeImmediatelyRPC should succeed")
			require.NotNil(t, result, "Should get response")

			// The execution might fail for various reasons (RPC limits, etc), but we're testing
			// that the execution context reflects the correct simulation mode
			t.Logf("   Execution success: %v", result.Success)
			if !result.Success {
				t.Logf("   Execution error (expected for some test environments): %s", result.Error)
			}

			// THE KEY ASSERTION: Verify execution context matches the requested isSimulated flag
			if result.ExecutionContext != nil {
				ctx := result.ExecutionContext.AsInterface()
				if ctxMap, ok := ctx.(map[string]interface{}); ok {
					// ExecutionContext uses snake_case field names
					actualIsSimulated, hasIsSimulated := ctxMap["is_simulated"]
					actualProvider, hasProvider := ctxMap["provider"]

					require.True(t, hasIsSimulated, "ExecutionContext should have is_simulated field")
					require.True(t, hasProvider, "ExecutionContext should have provider field")

					// This is the critical assertion that would have caught the bug
					assert.Equal(t, tt.expectedIsSimulated, actualIsSimulated,
						"ExecutionContext.isSimulated MUST match the input node config isSimulated flag. %s",
						tt.description)

					t.Logf("✅ ExecutionContext.isSimulated: %v (expected: %v)",
						actualIsSimulated, tt.expectedIsSimulated)
					t.Logf("✅ ExecutionContext.provider: %s (expected pattern: %s)",
						actualProvider, tt.expectedProviderPattern)

					// Note: We don't strictly check provider value because it can vary based on
					// execution success/failure, but isSimulated MUST always match the input
				} else {
					t.Fatal("ExecutionContext is not a map")
				}
			} else {
				t.Fatal("ExecutionContext should not be nil")
			}

			t.Logf("✅ Test passed: %s", tt.description)
		})
	}
}

// TestRunNodeWithInputsDefaultsToSimulation verifies that when isSimulated is not
// explicitly set, the system defaults to simulation mode for safety.
func TestRunNodeWithInputsDefaultsToSimulation(t *testing.T) {
	// Setup test environment
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	config := testutil.GetAggregatorConfig()
	engine := New(db, config, nil, testutil.GetLogger())

	smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

	// Create test user
	ownerAddr, ok := testutil.MustGetTestOwnerAddress()
	if !ok {
		t.Skip("Owner EOA address not set, skipping test")
	}
	ownerEOA := *ownerAddr
	factory := testutil.GetAggregatorConfig().SmartWallet.FactoryAddress

	// Connect to RPC client
	client, err := ethclient.Dial(config.SmartWallet.EthRpcUrl)
	require.NoError(t, err, "Failed to connect to RPC")
	defer client.Close()

	// Derive smart wallet address
	aa.SetFactoryAddress(factory)
	runnerAddr, err := aa.GetSenderAddress(client, ownerEOA, big.NewInt(0))
	require.NoError(t, err, "Failed to derive smart wallet address")

	user := &model.User{Address: ownerEOA}

	// Seed wallet in DB
	_ = StoreWallet(db, ownerEOA, &model.SmartWallet{
		Owner:   &ownerEOA,
		Address: runnerAddr,
		Factory: &factory,
		Salt:    big.NewInt(0),
	})

	// Create ContractWrite node WITHOUT explicitly setting isSimulated
	// (it should default to true for safety)
	contractAbi := []*structpb.Value{
		structpb.NewStructValue(&structpb.Struct{
			Fields: map[string]*structpb.Value{
				"inputs": structpb.NewListValue(&structpb.ListValue{
					Values: []*structpb.Value{
						structpb.NewStructValue(&structpb.Struct{
							Fields: map[string]*structpb.Value{
								"name": structpb.NewStringValue("approve"),
								"type": structpb.NewStringValue("function"),
							},
						}),
					},
				}),
				"name":            structpb.NewStringValue("approve"),
				"stateMutability": structpb.NewStringValue("nonpayable"),
				"type":            structpb.NewStringValue("function"),
			},
		}),
	}

	value := "0"
	gasLimit := "210000"
	// Note: IsSimulated is NOT set (nil pointer), should default to true

	contractWriteNode := &avsproto.TaskNode{
		Id:   "test-default-simulation",
		Name: "approve",
		Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
		TaskType: &avsproto.TaskNode_ContractWrite{
			ContractWrite: &avsproto.ContractWriteNode{
				Config: &avsproto.ContractWriteNode_Config{
					ContractAddress: "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",
					ContractAbi:     contractAbi,
					MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
						{
							MethodName:   "approve",
							MethodParams: []string{"0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E", "1000000"},
						},
					},
					// IsSimulated intentionally omitted (nil)
					Value:    &value,
					GasLimit: &gasLimit,
				},
			},
		},
	}

	req := &avsproto.RunNodeWithInputsReq{
		Node: contractWriteNode,
	}

	settingsData := map[string]interface{}{
		"runner":   runnerAddr.Hex(),
		"chain_id": 11155111,
	}

	req.InputVariables = make(map[string]*structpb.Value)
	settingsVal, err := structpb.NewValue(settingsData)
	require.NoError(t, err)
	req.InputVariables["settings"] = settingsVal

	t.Logf("🧪 Testing RunNodeWithInputs with isSimulated NOT SET (should default to true)")

	// Execute via RPC layer
	result, err := engine.RunNodeImmediatelyRPC(user, req)

	// Assertions
	require.NoError(t, err, "RunNodeImmediatelyRPC should succeed")
	require.NotNil(t, result, "Should get response")

	// Verify execution context defaults to simulation
	if result.ExecutionContext != nil {
		ctx := result.ExecutionContext.AsInterface()
		if ctxMap, ok := ctx.(map[string]interface{}); ok {
			// ExecutionContext uses snake_case field names
			actualIsSimulated, hasIsSimulated := ctxMap["is_simulated"]

			require.True(t, hasIsSimulated, "ExecutionContext should have is_simulated field")

			// When not explicitly set, should default to true (simulation) for safety
			assert.Equal(t, true, actualIsSimulated,
				"When isSimulated is not set, it should default to true (simulation mode) for safety")

			t.Logf("✅ Default simulation mode verified: isSimulated=%v", actualIsSimulated)
		}
	} else {
		t.Fatal("ExecutionContext should not be nil")
	}

	t.Logf("✅ Test passed: System correctly defaults to simulation mode when isSimulated is not specified")
}
