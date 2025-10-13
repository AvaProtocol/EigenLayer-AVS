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

// Contract addresses on Base mainnet
const (
	BASE_USDC        = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913" // Native USDC on Base
	BASE_WETH        = "0x4200000000000000000000000000000000000006" // WETH on Base
	BASE_SWAPROUTER  = "0x2626664c2603336E57B271c5C0b26F421741e481" // SwapRouter02
	BASE_QUOTER      = "0x3d4e44Eb1374240CE5F1B871ab261CD16335B76a" // QuoterV2
	BASE_ENTRYPOINT  = "0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789" // EntryPoint v0.6
	BASE_FACTORY     = "0xB99BC2E399e06CddCF5E725c0ea341E8f0322834" // SimpleAccountFactory
	BASE_SWAP_AMOUNT = "1000000"                                    // 1 USDC (6 decimals)
	BASE_FEE_TIER    = 3000                                         // 0.3%
	BASE_CHAIN_ID    = 8453                                         // Base mainnet chain ID
)

// TestRunNodeImmediately_UniswapSwap_Base tests the full approve + swap flow on Base mainnet
func TestRunNodeImmediately_UniswapSwap_Base(t *testing.T) {
	// Skip in short mode
	if testing.Short() {
		t.Skip("Skipping real transaction test in short mode")
	}

	// Load Base aggregator config first to get RPC endpoints
	baseAggregatorCfg, err := config.NewConfig("../../config/aggregator-base.yaml")
	require.NoError(t, err, "Failed to load Base aggregator config")

	// Skip if not running on Base (chain ID 8453)
	tempClient, err := ethclient.Dial(baseAggregatorCfg.SmartWallet.EthRpcUrl)
	if err != nil {
		t.Skipf("Cannot connect to Base RPC: %v", err)
	}
	chainID, err := tempClient.ChainID(context.Background())
	tempClient.Close()
	if err != nil {
		t.Skipf("Cannot get chain ID from RPC: %v", err)
	}
	if chainID.Int64() != BASE_CHAIN_ID {
		t.Skipf("Test requires Base network connection (current chain ID: %d)", chainID.Int64())
	}

	// Architecture: The smart wallet address is derived from the OWNER's EOA (TEST_PRIVATE_KEY)
	// but the UserOperation is SIGNED by the CONTROLLER's private key (from config)
	// This allows the controller to automate transactions on behalf of the owner's smart wallet
	privateKeyHex := os.Getenv("TEST_PRIVATE_KEY")
	if privateKeyHex == "" {
		t.Skip("TEST_PRIVATE_KEY not set, skipping real execution test")
	}

	// Parse owner's EOA private key (used to derive smart wallet address)
	privateKey, err := crypto.HexToECDSA(privateKeyHex[2:]) // Remove 0x prefix
	require.NoError(t, err, "Failed to parse TEST_PRIVATE_KEY")

	ownerAddress := crypto.PubkeyToAddress(privateKey.PublicKey)
	controllerAddress := crypto.PubkeyToAddress(baseAggregatorCfg.SmartWallet.ControllerPrivateKey.PublicKey)

	t.Logf("📋 Base Test Configuration:")
	t.Logf("   Owner EOA (derives wallet address): %s", ownerAddress.Hex())
	t.Logf("   Controller (signs UserOps): %s", controllerAddress.Hex())
	t.Logf("   Factory: %s", BASE_FACTORY)
	t.Logf("   Chain: Base (ID: %d)", BASE_CHAIN_ID)

	// Set factory for AA library
	aa.SetFactoryAddress(common.HexToAddress(BASE_FACTORY))
	t.Logf("   Computing salt:0 smart wallet address...")

	// Connect to Base using config
	client, err := ethclient.Dial(baseAggregatorCfg.SmartWallet.EthRpcUrl)
	require.NoError(t, err, "Failed to connect to Base")
	defer client.Close()

	// Compute the salt:0 smart wallet address for this owner
	smartWalletAddr, err := aa.GetSenderAddress(client, ownerAddress, big.NewInt(0))
	require.NoError(t, err, "Failed to compute smart wallet address")

	t.Logf("   ✅ Smart Wallet (salt:0): %s", smartWalletAddr.Hex())

	// Check balances
	ethBalance, err := client.BalanceAt(context.Background(), *smartWalletAddr, nil)
	require.NoError(t, err, "Failed to get ETH balance")
	t.Logf("   ETH Balance: %s wei", ethBalance.String())

	// Create properly initialized engine using Base aggregator config
	db := testutil.TestMustDB()
	t.Cleanup(func() {
		storage.Destroy(db.(*storage.BadgerStorage))
	})

	// Use the Base aggregator config directly (already loaded above)
	engine := New(db, baseAggregatorCfg, nil, testutil.GetLogger())
	t.Cleanup(func() {
		engine.Stop()
	})

	// Create user model
	user := &model.User{
		Address:             ownerAddress,
		SmartAccountAddress: smartWalletAddr,
	}

	// Register the smart wallet in the database
	factory := common.HexToAddress(BASE_FACTORY)
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
		"chain_id": int64(BASE_CHAIN_ID),
		"amount":   BASE_SWAP_AMOUNT,
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

	t.Run("Step1_ApproveUSDCToSwapRouter_Base", func(t *testing.T) {
		t.Logf("✍️  Approving USDC to SwapRouter on Base with REAL execution...")

		approvalConfig := map[string]interface{}{
			"contractAddress": BASE_USDC,
			"contractAbi":     erc20ABI,
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName":   "approve",
					"methodParams": []interface{}{BASE_SWAPROUTER, BASE_SWAP_AMOUNT},
					"callData":     "",
				},
			},
		}

		inputVars := map[string]interface{}{
			"settings": settings,
		}

		t.Logf("   Calling RunNodeImmediately with isSimulated=false on Base...")
		result, err := engine.RunNodeImmediately("contractWrite", approvalConfig, inputVars, user, false)

		require.NoError(t, err, "Approval RunNodeImmediately should not return error")
		require.NotNil(t, result, "Approval result should not be nil")

		// Check if the execution succeeded (result contains success/error fields)
		if successVal, ok := result["success"]; ok {
			if success, isBool := successVal.(bool); isBool && !success {
				// Execution failed - check for error message
				if errorVal, hasError := result["error"]; hasError {
					if errorStr, isString := errorVal.(string); isString {
						t.Fatalf("❌ Approval execution failed: %s", errorStr)
					}
				}
				t.Fatalf("❌ Approval execution failed with success=false")
			}
		}

		t.Logf("✅ Approval result: %+v", result)
		t.Logf("   Approval transaction submitted successfully on Base")

		// Wait for state propagation
		t.Logf("   ⏰ Waiting 15 seconds for Base state sync...")
		time.Sleep(15 * time.Second)
		t.Logf("   ✅ Wait complete")
	})

	t.Run("Step2_ExecuteSwap_Base", func(t *testing.T) {
		t.Logf("🔄 Executing USDC → WETH swap on Base with REAL execution...")

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
			"contractAddress": BASE_SWAPROUTER,
			"contractAbi":     swapRouterABI,
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName": "exactInputSingle",
					"methodParams": []interface{}{
						fmt.Sprintf(`["%s", "%s", "%d", "%s", "%s", "%s", 0]`,
							BASE_USDC,
							BASE_WETH,
							BASE_FEE_TIER,
							(*smartWalletAddr).Hex(),
							BASE_SWAP_AMOUNT,
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
		t.Logf("     tokenIn: %s (USDC)", BASE_USDC)
		t.Logf("     tokenOut: %s (WETH)", BASE_WETH)
		t.Logf("     fee: %d (0.3%%)", BASE_FEE_TIER)
		t.Logf("     amountIn: %s (1 USDC)", BASE_SWAP_AMOUNT)
		t.Logf("     amountOutMinimum: %s", minOutput)

		t.Logf("   Calling RunNodeImmediately with isSimulated=false on Base...")
		result, err := engine.RunNodeImmediately("contractWrite", swapConfig, inputVars, user, false)

		require.NoError(t, err, "Swap RunNodeImmediately should not return error")
		require.NotNil(t, result, "Swap result should not be nil")

		// Check if the execution succeeded (result contains success/error fields)
		if successVal, ok := result["success"]; ok {
			if success, isBool := successVal.(bool); isBool && !success {
				// Execution failed - check for error message
				if errorVal, hasError := result["error"]; hasError {
					if errorStr, isString := errorVal.(string); isString {
						t.Fatalf("❌ Swap execution failed: %s", errorStr)
					}
				}
				t.Fatalf("❌ Swap execution failed with success=false")
			}
		}

		t.Logf("✅ Swap result: %+v", result)

		// Extract transaction hash
		if data, ok := result["data"].(map[string]interface{}); ok {
			t.Logf("   Swap data: %+v", data)
		}

		t.Logf("🎉 SUCCESS: Smart wallet swap worked on Base!")
	})
}
