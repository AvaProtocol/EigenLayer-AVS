package taskengine

import (
	"fmt"
	"math/big"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
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
		_ = StoreWallet(db, int64(1), ownerEOA, &model.SmartWallet{
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

	t.Run("ETHTransfer_ConfigViaInputVariables", func(t *testing.T) {
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
		_ = StoreWallet(db, int64(1), ownerEOA, &model.SmartWallet{
			Owner:   &ownerEOA,
			Address: runnerAddr,
			Factory: &factory,
			Salt:    big.NewInt(0),
		})

		// Destination address for ETH transfer
		destinationAddr := "0x742d35Cc6634C0532925a3b8D4C9db96C4b4d8b6"
		amount := "1000000000000000000" // 1 ETH in wei

		// Create ETHTransfer node with config
		ethTransferNode := &avsproto.TaskNode{
			Id:   "test-eth-transfer",
			Name: "transfer1",
			Type: avsproto.NodeType_NODE_TYPE_ETH_TRANSFER,
			TaskType: &avsproto.TaskNode_EthTransfer{
				EthTransfer: &avsproto.ETHTransferNode{
					Config: &avsproto.ETHTransferNode_Config{
						Destination: destinationAddr,
						Amount:      amount,
					},
				},
			},
		}

		// Create protobuf request with the full TaskNode
		req := &avsproto.RunNodeWithInputsReq{
			Node: ethTransferNode,
		}

		// Settings for the workflow (including runner)
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

		t.Logf("🧪 Testing RunNodeImmediatelyRPC with ETHTransfer node:")
		t.Logf("   User (from JWT): %s", user.Address.Hex())
		t.Logf("   Runner (from settings): %s", runnerAddr.Hex())
		t.Logf("   Destination: %s", destinationAddr)
		t.Logf("   Amount: %s wei", amount)

		// Execute via RPC layer
		result, err := engine.RunNodeImmediatelyRPC(user, req)

		// Assertions
		require.NoError(t, err, "RunNodeImmediatelyRPC should succeed")
		require.NotNil(t, result, "Should get response")
		assert.True(t, result.Success, "ETH transfer should succeed in simulation")

		// Verify ETH transfer output data
		ethTransferOutput := result.GetEthTransfer()
		require.NotNil(t, ethTransferOutput, "Should have ETH transfer output")
		require.NotNil(t, ethTransferOutput.Data, "Should have ETH transfer data")

		// Extract data map
		dataMap := ethTransferOutput.Data.AsInterface()
		require.NotNil(t, dataMap, "Data should be convertible to interface")

		dataMapTyped, ok := dataMap.(map[string]interface{})
		require.True(t, ok, "Data should be a map")

		// Verify transfer object exists
		transferObj, hasTransfer := dataMapTyped["transfer"]
		require.True(t, hasTransfer, "Should have 'transfer' field in data")

		transferMap, ok := transferObj.(map[string]interface{})
		require.True(t, ok, "Transfer should be a map")

		// Verify transfer fields - THIS IS THE KEY ASSERTION
		// The 'from' field should be the runner (smart wallet) address
		fromField, hasFrom := transferMap["from"]
		require.True(t, hasFrom, "Transfer should have 'from' field")
		fromStr, ok := fromField.(string)
		require.True(t, ok, "From field should be a string")
		assert.NotEmpty(t, fromStr, "From field should not be empty")
		assert.Equal(t, runnerAddr.Hex(), fromStr, "From field should be the runner (smart wallet) address")

		// Verify to and value fields
		assert.Equal(t, destinationAddr, transferMap["to"], "To field should match destination")
		assert.Equal(t, amount, transferMap["value"], "Value field should match amount")

		t.Logf("✅ Transfer data validated:")
		t.Logf("   from: %s", fromStr)
		t.Logf("   to: %v", transferMap["to"])
		t.Logf("   value: %v", transferMap["value"])

		// Verify metadata contains transactionHash (success/isSimulated are in response/executionContext, not metadata)
		require.NotNil(t, result.Metadata, "Should have metadata")
		metadataMap := result.Metadata.AsInterface()
		metaTyped, ok := metadataMap.(map[string]interface{})
		require.True(t, ok, "Metadata should be a map")
		txHash, hasTxHash := metaTyped["transactionHash"]
		require.True(t, hasTxHash, "Metadata should have transactionHash")
		assert.NotEmpty(t, txHash, "transactionHash should not be empty")
		t.Logf("✅ Metadata validated:")
		t.Logf("   transactionHash: %v", txHash)

		t.Logf("✅ ETHTransfer RPC test completed successfully")
	})

	// ERC20Overrides_UniswapSwap drives the new erc20_overrides request field
	// end-to-end: it builds a RunNodeWithInputsReq with populated overrides and
	// calls the real RunNodeImmediatelyRPC entrypoint on a Uniswap swap node. This
	// exercises the whole new path — proto field → RunNodeImmediately threading →
	// SimulationStateMap → Tenderly state_objects — rather than calling
	// ApplyUserERC20Override directly.
	//
	// It uses a deterministic, never-funded owner so the counterfactual salt:0
	// runner is guaranteed to hold 0 USDC and 0 allowance. That makes the proof
	// self-contained and robust (no dependency on a shared wallet staying
	// funded/unapproved) and lets us isolate BOTH override paths:
	//
	//   1. no overrides      → reverts on the allowance precondition
	//   2. allowance only    → reverts on the *balance* precondition (so the
	//                          balance override is provably load-bearing)
	//   3. balance+allowance → swap simulates cleanly
	//
	// This requires Tenderly creds (config/test.yaml); per project convention for
	// integration tests we hard-fail rather than skip when they are missing.
	t.Run("ERC20Overrides_UniswapSwap", func(t *testing.T) {
		db := testutil.TestMustDB()
		defer storage.Destroy(db.(*storage.BadgerStorage))

		config := testutil.GetAggregatorConfig()
		require.NotEmpty(t, config.TenderlyAccessKey,
			"erc20_overrides E2E test requires Tenderly creds in config/test.yaml (tenderly_account/project/access_key)")
		engine := New(db, config, nil, testutil.GetLogger())

		// Deterministic throwaway owner — its salt:0 smart wallet has never held
		// USDC nor approved SwapRouter02 on Sepolia, so the control reverts
		// reliably and the balance override is never masked by a real balance.
		ownerEOA := common.HexToAddress("0xe8a78b476AE1403b7fD39b662545AE608Aced7c7")

		aa.SetFactoryAddress(config.SmartWallet.FactoryAddress)
		client, err := ethclient.Dial(config.SmartWallet.EthRpcUrl)
		require.NoError(t, err, "Failed to connect to RPC")
		defer client.Close()

		runnerAddr, err := aa.GetSenderAddress(client, ownerEOA, big.NewInt(0))
		require.NoError(t, err, "Failed to derive smart wallet address")

		user := &model.User{Address: ownerEOA}
		require.NoError(t, StoreWallet(db, int64(1), ownerEOA, &model.SmartWallet{
			Owner:   &ownerEOA,
			Address: runnerAddr,
			Factory: &config.SmartWallet.FactoryAddress,
			Salt:    big.NewInt(0),
		}))

		runner := runnerAddr.Hex()
		settingsVal, err := structpb.NewValue(map[string]interface{}{
			"runner":   runner,
			"chain_id": 11155111, // Sepolia
		})
		require.NoError(t, err)

		runSwap := func(overrides []*avsproto.ERC20StateOverride) *avsproto.RunNodeWithInputsResp {
			resp, err := engine.RunNodeImmediatelyRPC(user, &avsproto.RunNodeWithInputsReq{
				Node:           exactInputSingleSwapNode(t, runner),
				InputVariables: map[string]*structpb.Value{"settings": settingsVal},
				Erc20Overrides: overrides,
			})
			require.NoError(t, err, "RunNodeImmediatelyRPC should not error")
			require.NotNil(t, resp)
			return resp
		}

		// USDC on Sepolia is Circle's FiatToken: balanceOf at storage slot 9 and
		// allowance at slot 10 (confirmed empirically against the deployed token).
		maxAllowance := "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
		bigBalance := "0x38d7ea4c68000" // 1,000,000,000 USDC (6 decimals)
		balanceOverride := &avsproto.ERC20StateOverride{
			TokenAddress: overridesUSDC,
			OwnerAddress: runner,
			Balance:      strPtr(bigBalance),
			BalanceSlot:  u64Ptr(9),
		}
		allowanceOverride := &avsproto.ERC20StateOverride{
			TokenAddress:   overridesUSDC,
			OwnerAddress:   runner,
			SpenderAddress: strPtr(overridesSwapRouter02),
			Allowance:      strPtr(maxAllowance),
			AllowanceSlot:  u64Ptr(10),
		}

		// 1. No overrides → reverts because the runner has no SwapRouter02 approval.
		control := runSwap(nil)
		t.Logf("no overrides       : success=%v error=%q", control.Success, control.Error)
		require.False(t, control.Success, "unfunded/unapproved runner must not swap without overrides")
		require.True(t, isFundingOrApprovalRevert(control.Error),
			"control should revert on the allowance/balance precondition, got: "+control.Error)

		// 2. Allowance only → approval now satisfied, so it must revert on the
		//    *balance* precondition. This proves the balance override is necessary
		//    and that the allowance override already flowed through the RPC path.
		allowOnly := runSwap([]*avsproto.ERC20StateOverride{allowanceOverride})
		t.Logf("allowance only     : success=%v error=%q", allowOnly.Success, allowOnly.Error)
		require.False(t, allowOnly.Success, "swap must still fail with balance unseeded")
		assert.NotContains(t, allowOnly.Error, "transfer amount exceeds allowance",
			"allowance override should have seeded the approval through the RPC path")
		// The remaining revert must be the balance precondition. SwapRouter02's
		// transferFrom surfaces that either as the inner token revert
		// ("transfer amount exceeds balance") or as TransferHelper "STF",
		// depending on how Tenderly extracts the revert — accept either.
		balanceRevert := strings.Contains(allowOnly.Error, "transfer amount exceeds balance") ||
			strings.Contains(allowOnly.Error, "STF")
		assert.Truef(t, balanceRevert,
			"with approval seeded but balance unseeded, the swap must revert on the balance precondition (exceeds balance / STF), got: %q", allowOnly.Error)

		// 3. Balance + allowance → both preconditions seeded through the request,
		//    so the swap simulates cleanly. The flip from (2) to (3) is the
		//    decisive proof that the balance override reaches the Tenderly sim.
		result := runSwap([]*avsproto.ERC20StateOverride{balanceOverride, allowanceOverride})
		t.Logf("balance + allowance: success=%v error=%q", result.Success, result.Error)
		assert.NotContains(t, result.Error, "transfer amount exceeds allowance")
		assert.NotContains(t, result.Error, "transfer amount exceeds balance")
		assert.NotContains(t, result.Error, "STF",
			"Uniswap TransferHelper 'STF' means transferFrom failed — overrides should prevent it")
		assert.Truef(t, result.Success,
			"swap simulation should succeed once erc20_overrides seed balance+allowance, got error=%q", result.Error)
	})
}

// Sepolia Uniswap V3 + token addresses (shared with execute_uniswap_approval_test.go).
const (
	overridesUSDC         = "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238" // token1 (FiatToken: balance slot 9, allowance slot 10)
	overridesWETH         = "0xfff9976782d46cc05630d1f6ebab18b2324d6b14" // token0
	overridesSwapRouter02 = "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E"
	overridesFeeTier      = "3000"
	overridesAmountIn     = "100" // 0.0001 USDC (6 decimals)
)

func strPtr(s string) *string { return &s }
func u64Ptr(v uint64) *uint64 { return &v }

// isFundingOrApprovalRevert reports whether a simulation error is the
// ERC20 funding/approval precondition that erc20_overrides exist to seed —
// either the OpenZeppelin-style messages or Uniswap's TransferHelper "STF".
func isFundingOrApprovalRevert(errMsg string) bool {
	return strings.Contains(errMsg, "transfer amount exceeds allowance") ||
		strings.Contains(errMsg, "transfer amount exceeds balance") ||
		strings.Contains(errMsg, "STF")
}

// exactInputSingleSwapNode builds a SwapRouter02.exactInputSingle contractWrite
// node that pulls `amountIn` USDC from the runner via transferFrom — the exact
// path that reverts with "transfer amount exceeds allowance/balance" (Uniswap
// surfaces it as "STF") unless the runner is funded and has approved the router.
func exactInputSingleSwapNode(t *testing.T, runner string) *avsproto.TaskNode {
	t.Helper()
	abi, err := structpb.NewValue(map[string]interface{}{
		"inputs": []interface{}{
			map[string]interface{}{
				"name": "params",
				"type": "tuple",
				"components": []interface{}{
					map[string]interface{}{"name": "tokenIn", "type": "address"},
					map[string]interface{}{"name": "tokenOut", "type": "address"},
					map[string]interface{}{"name": "fee", "type": "uint24"},
					map[string]interface{}{"name": "recipient", "type": "address"},
					map[string]interface{}{"name": "amountIn", "type": "uint256"},
					map[string]interface{}{"name": "amountOutMinimum", "type": "uint256"},
					map[string]interface{}{"name": "sqrtPriceLimitX96", "type": "uint160"},
				},
			},
		},
		"name":            "exactInputSingle",
		"outputs":         []interface{}{map[string]interface{}{"name": "amountOut", "type": "uint256"}},
		"stateMutability": "payable",
		"type":            "function",
	})
	require.NoError(t, err, "failed to build exactInputSingle ABI value")

	// amountOutMinimum=0 so the swap can't revert on slippage — keeps the test
	// focused on the allowance/balance precondition the overrides target.
	params := fmt.Sprintf(`["%s", "%s", "%s", "%s", "%s", "0", 0]`,
		overridesUSDC, overridesWETH, overridesFeeTier, runner, overridesAmountIn)

	return &avsproto.TaskNode{
		Id:   "execute_swap",
		Name: "execute_swap",
		Type: avsproto.NodeType_NODE_TYPE_CONTRACT_WRITE,
		TaskType: &avsproto.TaskNode_ContractWrite{
			ContractWrite: &avsproto.ContractWriteNode{
				Config: &avsproto.ContractWriteNode_Config{
					ContractAddress: overridesSwapRouter02,
					ContractAbi:     []*structpb.Value{abi},
					MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
						{MethodName: "exactInputSingle", MethodParams: []string{params}},
					},
				},
			},
		},
	}
}
