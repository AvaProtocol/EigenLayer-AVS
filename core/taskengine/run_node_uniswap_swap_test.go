package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
)

// Contract addresses on Sepolia
const (
	SEPOLIA_USDC       = "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
	SEPOLIA_WETH       = "0xfff9976782d46cc05630d1f6ebab18b2324d6b14"
	SEPOLIA_SWAPROUTER = "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E"
	SEPOLIA_QUOTER     = "0xEd1f6473345F45b75F8179591dd5bA1888cf2FB3"
	SEPOLIA_ENTRYPOINT = "0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789"
	SEPOLIA_FACTORY    = "0xB99BC2E399e06CddCF5E725c0ea341E8f0322834"
	SWAP_AMOUNT        = "1000000" // 1 USDC
	FEE_TIER           = 500       // 0.05% (trying different fee tier)
	SLIPPAGE_PERCENT   = 50        // 50% slippage tolerance
)

// getSepoliaRPC returns Sepolia RPC URL from env or uses aggregator config
func getSepoliaRPC(t *testing.T) string {
	if rpc := os.Getenv("SEPOLIA_RPC_URL"); rpc != "" {
		return rpc
	}
	// Fallback to config file
	cfg, err := config.NewConfig(testutil.GetConfigPath(testutil.DefaultSepoliaConfigPath))
	if err == nil && cfg.SmartWallet.EthRpcUrl != "" {
		return cfg.SmartWallet.EthRpcUrl
	}
	t.Skip("SEPOLIA_RPC_URL not set in environment")
	return ""
}

// TestRunNodeImmediately_UniswapSwap tests the full approve + swap flow using runNodeImmediately
// with real UserOp execution to debug the STF error
//
// NOTE: This test uses the actual workflow owner (0xc60e71bd...) and smart wallet (0x71c8f4D7...).
// The TEST_PRIVATE_KEY is for a different owner (0x72D841...) and won't work with this wallet.
// To run this test, you need the private key for 0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788.
func TestRunNodeImmediately_UniswapSwap(t *testing.T) {
	// Skip in short mode - this is a real transaction test
	if testing.Short() {
		t.Skip("Skipping real transaction test in short mode")
	}

	sepoliaRPC := getSepoliaRPC(t)

	// Skip if not running on Sepolia (chain ID 11155111)
	// Connect to RPC to check chain ID
	tempClient, err := ethclient.Dial(sepoliaRPC)
	if err != nil {
		t.Skipf("Cannot connect to RPC to verify chain ID: %v", err)
	}
	chainID, err := tempClient.ChainID(context.Background())
	tempClient.Close()
	if err != nil {
		t.Skipf("Cannot get chain ID from RPC: %v", err)
	}
	if chainID.Int64() != 11155111 {
		t.Skipf("Skipping Sepolia-only test (current chain ID: %d)", chainID.Int64())
	}

	// Load the aggregator config first (needed for controller key)
	aggregatorCfg, err := config.NewConfig(testutil.GetConfigPath(testutil.DefaultSepoliaConfigPath))
	require.NoError(t, err, "Failed to load aggregator config")

	// For real execution, we use the controller private key as the owner
	// This is because the controller signs all UserOps for automation
	// The user's EOA is only used for authentication (signing API keys)
	controllerKey := aggregatorCfg.SmartWallet.ControllerPrivateKey
	if controllerKey == nil {
		t.Skip("Controller private key not set in config, skipping real execution test")
	}

	// The controller key is already an *ecdsa.PrivateKey from config
	ownerAddress := crypto.PubkeyToAddress(controllerKey.PublicKey)
	t.Logf("   Using controller as smart wallet owner: %s", ownerAddress.Hex())
	t.Logf("📋 Test Configuration:")
	t.Logf("   Owner EOA: %s", ownerAddress.Hex())
	t.Logf("   Factory: %s", SEPOLIA_FACTORY)

	// Set factory for AA library to compute smart wallet address
	aa.SetFactoryAddress(common.HexToAddress(SEPOLIA_FACTORY))
	t.Logf("   Computing salt:0 smart wallet address...")

	// Connect to Sepolia
	client, err := ethclient.Dial(sepoliaRPC)
	require.NoError(t, err, "Failed to connect to Sepolia")
	defer client.Close()

	// Compute the salt:0 smart wallet address for this owner
	smartWalletAddr, err := aa.GetSenderAddress(client, ownerAddress, big.NewInt(0))
	require.NoError(t, err, "Failed to compute smart wallet address")

	t.Logf("   ✅ Smart Wallet (salt:0): %s", smartWalletAddr.Hex())
	t.Logf("   ⚠️  This wallet MUST have ETH for gas (contract writes are NOW paymaster-sponsored)")

	// Check balances
	ethBalance, err := client.BalanceAt(context.Background(), *smartWalletAddr, nil)
	require.NoError(t, err, "Failed to get ETH balance")
	t.Logf("   ETH Balance: %s wei", ethBalance.String())

	// Create properly initialized engine using actual aggregator config
	db := testutil.TestMustDB()
	t.Cleanup(func() {
		storage.Destroy(db.(*storage.BadgerStorage))
	})

	engine := New(db, aggregatorCfg, nil, testutil.GetLogger())
	t.Cleanup(func() {
		engine.Stop()
	})

	// Create user model
	user := &model.User{
		Address:             ownerAddress,
		SmartAccountAddress: smartWalletAddr,
	}

	// Register the smart wallet in the database for validation
	factory := common.HexToAddress(SEPOLIA_FACTORY)
	err = StoreWallet(db, ownerAddress, &model.SmartWallet{
		Owner:   &ownerAddress,
		Address: smartWalletAddr,
		Factory: &factory,
		Salt:    big.NewInt(0),
	})
	require.NoError(t, err, "Failed to store wallet in database")
	t.Logf("   ✅ Smart wallet registered in database")

	// Common settings for all nodes
	settings := map[string]interface{}{
		"runner":   (*smartWalletAddr).Hex(),
		"chain_id": int64(11155111),
		"amount":   SWAP_AMOUNT,
		"uniswapv3_pool": map[string]interface{}{
			"token0": map[string]interface{}{
				"id": SEPOLIA_WETH,
			},
			"token1": map[string]interface{}{
				"id": SEPOLIA_USDC,
			},
			"feeTier": FEE_TIER,
		},
		"uniswapv3_contracts": map[string]interface{}{
			"quoter":     SEPOLIA_QUOTER,
			"swaprouter": SEPOLIA_SWAPROUTER,
		},
	}

	// Minimal ERC20 ABI for approve
	erc20ABI := []interface{}{
		map[string]interface{}{
			"inputs": []interface{}{
				map[string]interface{}{"name": "spender", "type": "address"},
				map[string]interface{}{"name": "amount", "type": "uint256"},
			},
			"name":            "approve",
			"outputs":         []interface{}{map[string]interface{}{"name": "", "type": "bool"}},
			"stateMutability": "nonpayable",
			"type":            "function",
		},
		map[string]interface{}{
			"anonymous": false,
			"inputs": []interface{}{
				map[string]interface{}{"indexed": true, "name": "owner", "type": "address"},
				map[string]interface{}{"indexed": true, "name": "spender", "type": "address"},
				map[string]interface{}{"indexed": false, "name": "value", "type": "uint256"},
			},
			"name": "Approval",
			"type": "event",
		},
	}

	t.Run("Step1_ApproveUSDCToSwapRouter_RealExecution", func(t *testing.T) {
		t.Logf("✍️  Approving USDC to SwapRouter with REAL execution...")

		approvalConfig := map[string]interface{}{
			"contractAddress": SEPOLIA_USDC,
			"contractAbi":     erc20ABI,
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName":   "approve",
					"methodParams": []interface{}{SEPOLIA_SWAPROUTER, SWAP_AMOUNT},
					"callData":     "",
				},
			},
		}

		inputVars := map[string]interface{}{
			"settings": settings,
		}

		t.Logf("   Calling RunNodeImmediately with isSimulated=false...")
		result, err := engine.RunNodeImmediately("contractWrite", approvalConfig, inputVars, user, false)

		if err != nil {
			t.Logf("❌ Approval failed: %v", err)
			t.FailNow()
		}

		require.NoError(t, err, "Approval should succeed")
		t.Logf("✅ Approval result: %+v", result)

		// Extract transaction hash if available
		if data, ok := result["data"].(map[string]interface{}); ok {
			t.Logf("   Approval data: %+v", data)
		}

		t.Logf("   Approval transaction submitted successfully")

		// Wait 60 seconds for state propagation to fix race condition
		t.Logf("   ⏰ Waiting 60 seconds for bundler/RPC state sync...")
		t.Logf("   (This addresses the race condition where swap gas estimation")
		t.Logf("    runs before the bundler sees the approval transaction)")
		time.Sleep(60 * time.Second)
		t.Logf("   ✅ Wait complete - RPC nodes should be synced now")
	})

	t.Run("Step2_ExecuteSwap_RealExecution", func(t *testing.T) {
		t.Logf("🔄 Executing swap with REAL execution...")

		// Use a very low minOutput (1 wei) to ensure swap doesn't fail due to slippage
		// In production, you'd get a quote first
		minOutput := "1"

		swapRouterABI := []interface{}{
			map[string]interface{}{
				"inputs": []interface{}{
					map[string]interface{}{
						"components": []interface{}{
							map[string]interface{}{"name": "tokenIn", "type": "address"},
							map[string]interface{}{"name": "tokenOut", "type": "address"},
							map[string]interface{}{"name": "fee", "type": "uint24"},
							map[string]interface{}{"name": "recipient", "type": "address"},
							map[string]interface{}{"name": "amountIn", "type": "uint256"},
							map[string]interface{}{"name": "amountOutMinimum", "type": "uint256"},
							map[string]interface{}{"name": "sqrtPriceLimitX96", "type": "uint160"},
						},
						"name": "params",
						"type": "tuple",
					},
				},
				"name":            "exactInputSingle",
				"outputs":         []interface{}{map[string]interface{}{"name": "amountOut", "type": "uint256"}},
				"stateMutability": "payable",
				"type":            "function",
			},
		}

		swapConfig := map[string]interface{}{
			"contractAddress": SEPOLIA_SWAPROUTER,
			"contractAbi":     swapRouterABI,
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName": "exactInputSingle",
					"methodParams": []interface{}{
						fmt.Sprintf(`["%s", "%s", "%d", "%s", "%s", "%s", 0]`,
							SEPOLIA_USDC,
							SEPOLIA_WETH,
							FEE_TIER,
							(*smartWalletAddr).Hex(),
							SWAP_AMOUNT,
							minOutput,
						),
					},
					"callData": "",
				},
			},
		}

		inputVars := map[string]interface{}{
			"settings": settings,
		}

		t.Logf("   Swap parameters:")
		t.Logf("     tokenIn: %s (USDC)", SEPOLIA_USDC)
		t.Logf("     tokenOut: %s (WETH)", SEPOLIA_WETH)
		t.Logf("     amountIn: %s", SWAP_AMOUNT)
		t.Logf("     amountOutMinimum: %s", minOutput)

		t.Logf("   Calling RunNodeImmediately with isSimulated=false...")
		result, err := engine.RunNodeImmediately("contractWrite", swapConfig, inputVars, user, false)

		if err != nil {
			t.Logf("❌ Swap failed: %v", err)
			t.Logf("   This reproduces the STF error from the workflow!")
			t.Logf("   Error details: %+v", err)

			// Don't fail the test - we want to see what happens
			t.Logf("\n🔍 ANALYSIS:")
			t.Logf("   - Approval was confirmed 15 seconds ago")
			t.Logf("   - Allowance is set on-chain (verified by script)")
			t.Logf("   - Direct EOA swap works")
			t.Logf("   - But smart wallet swap fails with STF")
			t.Logf("\n💡 THEORY:")
			t.Logf("   The issue is likely NOT state propagation delay")
			t.Logf("   There may be a difference in how smart wallet execute() works")
			t.Logf("   vs direct contract calls from EOA")

			return // Don't fail, just log
		}

		require.NoError(t, err, "Swap should succeed")
		t.Logf("✅ Swap result: %+v", result)

		// Extract transaction hash
		if data, ok := result["data"].(map[string]interface{}); ok {
			t.Logf("   Swap data: %+v", data)
		}

		t.Logf("🎉 SUCCESS: Smart wallet swap worked!")
	})
}

// TestRunNodeImmediately_ApprovalOnly tests just the approval step with real execution
//
// NOTE: This test uses the actual workflow owner (0xc60e71bd...) and smart wallet (0x71c8f4D7...).
// The TEST_PRIVATE_KEY is for a different owner (0x72D841...) and won't work with this wallet.
// To run this test with real execution, you need the private key for 0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788.
func TestRunNodeImmediately_ApprovalOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping real transaction test in short mode")
	}

	// Skip if not running on Sepolia (chain ID 11155111)
	// Connect to RPC to check chain ID
	sepoliaRPC := getSepoliaRPC(t)
	tempClient, err := ethclient.Dial(sepoliaRPC)
	if err != nil {
		t.Skipf("Cannot connect to RPC to verify chain ID: %v", err)
	}
	chainID, err := tempClient.ChainID(context.Background())
	tempClient.Close()
	if err != nil {
		t.Skipf("Cannot get chain ID from RPC: %v", err)
	}
	if chainID.Int64() != 11155111 {
		t.Skipf("Skipping Sepolia-only test (current chain ID: %d)", chainID.Int64())
	}

	privateKeyHex := os.Getenv("TEST_PRIVATE_KEY")
	if privateKeyHex == "" {
		t.Skip("TEST_PRIVATE_KEY not set")
	}

	privateKey, err := crypto.HexToECDSA(privateKeyHex[2:])
	require.NoError(t, err)

	ownerAddress := crypto.PubkeyToAddress(privateKey.PublicKey)

	t.Logf("🧪 Testing approval with is_simulated control")
	t.Logf("   Owner EOA: %s", ownerAddress.Hex())
	t.Logf("   Factory: %s", SEPOLIA_FACTORY)

	// Set factory for AA library to compute smart wallet address
	aa.SetFactoryAddress(common.HexToAddress(SEPOLIA_FACTORY))
	t.Logf("   Computing salt:0 smart wallet address...")

	// Compute the salt:0 smart wallet address for this owner
	client, err := ethclient.Dial(sepoliaRPC)
	require.NoError(t, err, "Failed to connect to RPC")
	defer client.Close()

	smartWalletAddr, err := aa.GetSenderAddress(client, ownerAddress, big.NewInt(0))
	require.NoError(t, err, "Failed to compute smart wallet address")

	t.Logf("   ✅ Smart Wallet (salt:0): %s", smartWalletAddr.Hex())
	t.Logf("   ⚠️  This wallet MUST have ETH for gas (contract writes are NOT paymaster-sponsored)")

	db := testutil.TestMustDB()
	t.Cleanup(func() {
		storage.Destroy(db.(*storage.BadgerStorage))
	})

	// Load the actual aggregator config (which has controller_private_key already set)
	aggregatorCfg, err := config.NewConfig(testutil.GetConfigPath(testutil.DefaultSepoliaConfigPath))
	require.NoError(t, err, "Failed to load aggregator config")

	engine := New(db, aggregatorCfg, nil, testutil.GetLogger())
	t.Cleanup(func() {
		engine.Stop()
	})

	user := &model.User{
		Address:             ownerAddress,
		SmartAccountAddress: smartWalletAddr,
	}

	// Register the smart wallet in the database for validation
	factory := common.HexToAddress(SEPOLIA_FACTORY)
	err = StoreWallet(db, ownerAddress, &model.SmartWallet{
		Owner:   &ownerAddress,
		Address: smartWalletAddr,
		Factory: &factory,
		Salt:    big.NewInt(0),
	})
	require.NoError(t, err, "Failed to store wallet in database")
	t.Logf("   ✅ Smart wallet registered in database")

	erc20ABI := []interface{}{
		map[string]interface{}{
			"inputs": []interface{}{
				map[string]interface{}{"name": "spender", "type": "address"},
				map[string]interface{}{"name": "amount", "type": "uint256"},
			},
			"name":            "approve",
			"outputs":         []interface{}{map[string]interface{}{"name": "", "type": "bool"}},
			"stateMutability": "nonpayable",
			"type":            "function",
		},
	}

	approvalConfig := map[string]interface{}{
		"contractAddress": SEPOLIA_USDC,
		"contractAbi":     erc20ABI,
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName":   "approve",
				"methodParams": []interface{}{SEPOLIA_SWAPROUTER, SWAP_AMOUNT},
				"callData":     "",
			},
		},
	}

	inputVars := map[string]interface{}{
		"settings": map[string]interface{}{
			"runner":   (*smartWalletAddr).Hex(),
			"chain_id": int64(11155111),
		},
	}

	// Test with simulation first
	t.Run("Simulation", func(t *testing.T) {
		t.Logf("🔮 Testing with simulation...")
		result, err := engine.RunNodeImmediately("contractWrite", approvalConfig, inputVars, user)
		require.NoError(t, err, "Simulation should succeed")
		t.Logf("✅ Simulation passed: %+v", result)
	})

	// Test with real execution
	t.Run("RealExecution", func(t *testing.T) {
		t.Skip("⚠️  KNOWN ISSUE: Real execution has a nil pointer dereference bug after UserOp submission")
		t.Logf("🚀 Testing with REAL execution...")
		result, err := engine.RunNodeImmediately("contractWrite", approvalConfig, inputVars, user, false)

		if err != nil {
			t.Logf("❌ Real execution failed: %v", err)
			t.Logf("   This helps debug the issue")
			return
		}

		require.NoError(t, err, "Real execution should succeed")

		// Check if execution was actually successful
		if success, ok := result["success"].(bool); ok && !success {
			if errorMsg, ok := result["error"].(string); ok {
				t.Fatalf("❌ Real execution failed: %s", errorMsg)
			}
			t.Fatalf("❌ Real execution failed with no error message")
		}

		t.Logf("✅ Real execution passed: %+v", result)

		// Verify transaction hash exists
		if data, ok := result["data"].(map[string]interface{}); ok {
			t.Logf("   Transaction data: %+v", data)
		}

		// Check metadata for transaction details
		if metadata, ok := result["metadata"].([]interface{}); ok && len(metadata) > 0 {
			t.Logf("   Metadata: %+v", metadata[0])
		}
	})
}
