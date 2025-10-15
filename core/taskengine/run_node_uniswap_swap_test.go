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
	SEPOLIA_FEE_TIER   = 500       // 0.05%
	SWAP_AMOUNT        = "1000000" // 1 USDC
	FEE_TIER           = 500       // 0.05% (alias for SEPOLIA_FEE_TIER, kept for backward compatibility)
	SLIPPAGE_PERCENT   = 50        // 50% slippage tolerance
)

// getSepoliaRPC returns Sepolia RPC URL from env or uses aggregator config
func getSepoliaRPC(t *testing.T) string {
	if rpc := os.Getenv("SEPOLIA_RPC_URL"); rpc != "" {
		return rpc
	}
	// Fallback to config file
	cfg, err := config.NewConfig(testutil.GetConfigPath(testutil.DefaultConfigPath))
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

	// Get TEST_PRIVATE_KEY to derive the owner's EOA and smart wallet address
	// (automatically loaded from .env file by testutil)
	ownerAddr, ok := testutil.MustGetTestOwnerAddress()
	if !ok {
		t.Skip("TEST_PRIVATE_KEY not set, skipping real execution test")
	}
	ownerAddress := *ownerAddr

	// Load the aggregator config (needed for factory, RPC, controller key for signing)
	aggregatorCfg, err := config.NewConfig(testutil.GetConfigPath(testutil.DefaultConfigPath))
	require.NoError(t, err, "Failed to load aggregator config")

	t.Logf("📋 Test Configuration:")
	t.Logf("   Owner EOA (derives wallet address): %s", ownerAddress.Hex())
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

	// ========================================
	// Step 1: Approve USDC to SwapRouter
	// ========================================
	t.Logf("✍️  Step 1: Approving USDC to SwapRouter on Sepolia with REAL execution...")

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

	t.Logf("   Calling RunNodeImmediately with isSimulated=false on Sepolia...")
	approvalResult, err := engine.RunNodeImmediately("contractWrite", approvalConfig, inputVars, user, false)

	require.NoError(t, err, "Approval RunNodeImmediately should not return error")
	require.NotNil(t, approvalResult, "Approval result should not be nil")

	// Check if the execution succeeded (result contains success/error fields)
	if successVal, ok := approvalResult["success"]; ok {
		if success, isBool := successVal.(bool); isBool && !success {
			// Execution failed - check for error message
			if errorVal, hasError := approvalResult["error"]; hasError {
				if errorStr, isString := errorVal.(string); isString {
					t.Fatalf("❌ Approval execution failed: %s", errorStr)
				}
			}
			t.Fatalf("❌ Approval execution failed with success=false")
		}
	}

	t.Logf("✅ Approval confirmed on-chain")
	t.Logf("   Result: %+v", approvalResult)

	// Wait 5 seconds to ensure approval is fully propagated across RPC nodes
	// This prevents gas estimation failures due to RPC node lag
	t.Logf("⏳ Waiting 5 seconds for approval to propagate across RPC nodes...")
	time.Sleep(5 * time.Second)

	// ========================================
	// Step 2: Execute Swap (only if approval succeeded)
	// ========================================
	t.Logf("🔄 Step 2: Executing USDC → WETH swap on Sepolia with REAL execution...")

	minOutput := "1" // 1 wei minimum output

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

	t.Logf("   Swap parameters:")
	t.Logf("     tokenIn: %s (USDC)", SEPOLIA_USDC)
	t.Logf("     tokenOut: %s (WETH)", SEPOLIA_WETH)
	t.Logf("     fee: %d (0.05%%)", FEE_TIER)
	t.Logf("     amountIn: %s (1 USDC)", SWAP_AMOUNT)
	t.Logf("     amountOutMinimum: %s", minOutput)

	t.Logf("   Calling RunNodeImmediately with isSimulated=false on Sepolia...")
	swapResult, err := engine.RunNodeImmediately("contractWrite", swapConfig, inputVars, user, false)

	require.NoError(t, err, "Swap RunNodeImmediately should not return error")
	require.NotNil(t, swapResult, "Swap result should not be nil")

	// Check if the execution succeeded (result contains success/error fields)
	if successVal, ok := swapResult["success"]; ok {
		if success, isBool := successVal.(bool); isBool && !success {
			// Execution failed - check for error message
			if errorVal, hasError := swapResult["error"]; hasError {
				if errorStr, isString := errorVal.(string); isString {
					t.Fatalf("❌ Swap execution failed: %s", errorStr)
				}
			}
			t.Fatalf("❌ Swap execution failed with success=false")
		}
	}

	t.Logf("✅ Swap confirmed on-chain")
	t.Logf("   Result: %+v", swapResult)

	// Extract transaction hash
	if data, ok := swapResult["data"].(map[string]interface{}); ok {
		t.Logf("   Swap data: %+v", data)
	}

	t.Logf("🎉 SUCCESS: Both approval and swap completed successfully on Sepolia!")
}
