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
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc20"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
)

// Contract addresses for Base mainnet
const (
	BASE_USDC        = "0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913" // Native USDC on Base
	BASE_WETH        = "0x4200000000000000000000000000000000000006" // WETH on Base
	BASE_SWAPROUTER  = "0x2626664c2603336E57B271c5C0b26F421741e481" // SwapRouter02
	BASE_QUOTER      = "0x3d4e44Eb1374240CE5F1B871ab261CD16335B76a" // QuoterV2
	BASE_ENTRYPOINT  = "0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789" // EntryPoint v0.6
	BASE_FACTORY     = "0xB99BC2E399e06CddCF5E725c0ea341E8f0322834" // SimpleAccountFactory
	BASE_SWAP_AMOUNT = "10000"                                      // 0.01 USDC (6 decimals) - tiny amount for repeated testing
	BASE_FEE_TIER    = 3000                                         // 0.3%
	BASE_CHAIN_ID    = 8453                                         // Base mainnet chain ID
)

// Contract addresses for Sepolia testnet
const (
	SEPOLIA_USDC        = "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
	SEPOLIA_WETH        = "0xfff9976782d46cc05630d1f6ebab18b2324d6b14"
	SEPOLIA_SWAPROUTER  = "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E"
	SEPOLIA_QUOTER      = "0xEd1f6473345F45b75F8179591dd5bA1888cf2FB3"
	SEPOLIA_ENTRYPOINT  = "0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789"
	SEPOLIA_FACTORY     = "0xB99BC2E399e06CddCF5E725c0ea341E8f0322834"
	SEPOLIA_SWAP_AMOUNT = "10000"  // 0.01 USDC (6 decimals) - tiny amount for repeated testing
	SEPOLIA_FEE_TIER    = 500      // 0.05%
	SEPOLIA_CHAIN_ID    = 11155111 // Sepolia testnet chain ID
)

// chainConfig contains chain-specific configuration
type chainConfig struct {
	name       string
	usdc       string
	weth       string
	swapRouter string
	quoter     string
	entrypoint string
	factory    string
	swapAmount string
	feeTier    int
	chainID    int64
	configFile string
}

// getChainConfig returns the appropriate chain configuration based on TEST_CHAIN env var
func getChainConfig() *chainConfig {
	testChain := os.Getenv("TEST_CHAIN")

	switch testChain {
	case "sepolia":
		return &chainConfig{
			name:       "Sepolia",
			usdc:       SEPOLIA_USDC,
			weth:       SEPOLIA_WETH,
			swapRouter: SEPOLIA_SWAPROUTER,
			quoter:     SEPOLIA_QUOTER,
			entrypoint: SEPOLIA_ENTRYPOINT,
			factory:    SEPOLIA_FACTORY,
			swapAmount: SEPOLIA_SWAP_AMOUNT,
			feeTier:    SEPOLIA_FEE_TIER,
			chainID:    SEPOLIA_CHAIN_ID,
			configFile: testutil.DefaultConfigPath,
		}
	case "base":
		return &chainConfig{
			name:       "Base",
			usdc:       BASE_USDC,
			weth:       BASE_WETH,
			swapRouter: BASE_SWAPROUTER,
			quoter:     BASE_QUOTER,
			entrypoint: BASE_ENTRYPOINT,
			factory:    BASE_FACTORY,
			swapAmount: BASE_SWAP_AMOUNT,
			feeTier:    BASE_FEE_TIER,
			chainID:    BASE_CHAIN_ID,
			configFile: "aggregator-base.yaml",
		}
	default:
		// Default to Base for backward compatibility
		return &chainConfig{
			name:       "Base",
			usdc:       BASE_USDC,
			weth:       BASE_WETH,
			swapRouter: BASE_SWAPROUTER,
			quoter:     BASE_QUOTER,
			entrypoint: BASE_ENTRYPOINT,
			factory:    BASE_FACTORY,
			swapAmount: BASE_SWAP_AMOUNT,
			feeTier:    BASE_FEE_TIER,
			chainID:    BASE_CHAIN_ID,
			configFile: "aggregator-base.yaml",
		}
	}
}

// uniswapSwapTestSetup contains shared setup for Uniswap swap tests
type uniswapSwapTestSetup struct {
	cfg               *config.Config
	chain             *chainConfig
	ownerAddress      common.Address
	controllerAddress common.Address
	smartWalletAddr   *common.Address
	client            *ethclient.Client
	engine            *Engine
	user              *model.User
}

// setupUniswapSwapTest performs common setup for Uniswap swap tests
func setupUniswapSwapTest(t *testing.T) *uniswapSwapTestSetup {
	// Skip in short mode
	if testing.Short() {
		t.Skip("Skipping real transaction test in short mode")
	}

	// Get chain configuration based on TEST_CHAIN env var
	chain := getChainConfig()

	// Load aggregator config - skip if not available
	aggregatorCfg, err := config.NewConfig(testutil.GetConfigPath(chain.configFile))
	if err != nil {
		t.Skipf("%s config not found - skipping %s test: %v", chain.configFile, chain.name, err)
	}

	// Skip if not running on the correct chain
	tempClient, err := ethclient.Dial(aggregatorCfg.SmartWallet.EthRpcUrl)
	if err != nil {
		t.Skipf("Cannot connect to %s RPC: %v", chain.name, err)
	}
	chainID, err := tempClient.ChainID(context.Background())
	tempClient.Close()
	if err != nil {
		t.Skipf("Cannot get chain ID from RPC: %v", err)
	}
	if chainID.Int64() != chain.chainID {
		t.Skipf("Test requires %s network connection (current chain ID: %d, expected: %d)", chain.name, chainID.Int64(), chain.chainID)
	}

	// Architecture: The smart wallet address is derived from the OWNER's EOA address
	// but the UserOperation is SIGNED by the CONTROLLER's private key (from config)
	// This allows the controller to automate transactions on behalf of the owner's smart wallet
	ownerAddr, ok := testutil.MustGetTestOwnerAddress()
	if !ok {
		t.Skip("Owner EOA address not set, skipping real execution test")
	}
	ownerAddress := *ownerAddr
	controllerAddress := aggregatorCfg.SmartWallet.ControllerAddress

	t.Logf("📋 %s Uniswap Swap Test Configuration:", chain.name)
	t.Logf("   Owner EOA (derives wallet address): %s", ownerAddress.Hex())
	t.Logf("   Controller (signs UserOps): %s", controllerAddress.Hex())
	t.Logf("   Factory: %s", chain.factory)
	t.Logf("   Chain: %s (ID: %d)", chain.name, chain.chainID)

	// Set factory for AA library
	aa.SetFactoryAddress(common.HexToAddress(chain.factory))
	t.Logf("   Computing salt:0 smart wallet address...")

	// Connect using config
	client, err := ethclient.Dial(aggregatorCfg.SmartWallet.EthRpcUrl)
	require.NoError(t, err, "Failed to connect to %s", chain.name)
	defer client.Close()

	// Compute the salt:0 smart wallet address for this owner
	smartWalletAddr, err := aa.GetSenderAddress(client, ownerAddress, big.NewInt(0))
	require.NoError(t, err, "Failed to compute smart wallet address")

	t.Logf("   ✅ Smart Wallet (salt:0): %s", smartWalletAddr.Hex())

	// Check balances
	ethBalance, err := client.BalanceAt(context.Background(), *smartWalletAddr, nil)
	require.NoError(t, err, "Failed to get ETH balance")
	t.Logf("   ETH Balance: %s wei", ethBalance.String())

	// Create properly initialized engine using aggregator config
	db := testutil.TestMustDB()
	t.Cleanup(func() {
		storage.Destroy(db.(*storage.BadgerStorage))
	})

	// Use the aggregator config (already loaded above)
	engine := New(db, aggregatorCfg, nil, testutil.GetLogger())
	t.Cleanup(func() {
		engine.Stop()
	})

	// Create user model
	user := &model.User{
		Address:             ownerAddress,
		SmartAccountAddress: smartWalletAddr,
	}

	// Register the smart wallet in the database
	factory := common.HexToAddress(chain.factory)
	err = StoreWallet(db, ownerAddress, &model.SmartWallet{
		Owner:   &ownerAddress,
		Address: smartWalletAddr,
		Factory: &factory,
		Salt:    big.NewInt(0),
	})
	require.NoError(t, err, "Failed to store wallet in database")
	t.Logf("   ✅ Smart wallet registered in database")

	return &uniswapSwapTestSetup{
		cfg:               aggregatorCfg,
		chain:             chain,
		ownerAddress:      ownerAddress,
		controllerAddress: controllerAddress,
		smartWalletAddr:   smartWalletAddr,
		client:            client,
		engine:            engine,
		user:              user,
	}
}

// checkUSDCAllowance checks the current USDC allowance for the smart wallet
func checkUSDCAllowance(t *testing.T, setup *uniswapSwapTestSetup) *big.Int {
	usdcContract, err := erc20.NewErc20(common.HexToAddress(setup.chain.usdc), setup.client)
	require.NoError(t, err, "Failed to create USDC contract binding")

	allowance, err := usdcContract.Allowance(nil, *setup.smartWalletAddr, common.HexToAddress(setup.chain.swapRouter))
	require.NoError(t, err, "Failed to get USDC allowance")

	t.Logf("🔍 Current USDC Allowance: %s (%.6f USDC)", allowance.String(), float64(allowance.Int64())/1e6)
	return allowance
}

// checkUSDCBalance checks the current USDC balance for the smart wallet
func checkUSDCBalance(t *testing.T, setup *uniswapSwapTestSetup) *big.Int {
	usdcContract, err := erc20.NewErc20(common.HexToAddress(setup.chain.usdc), setup.client)
	require.NoError(t, err, "Failed to create USDC contract binding")

	balance, err := usdcContract.BalanceOf(nil, *setup.smartWalletAddr)
	require.NoError(t, err, "Failed to get USDC balance")

	t.Logf("💰 Current USDC Balance: %s (%.6f USDC)", balance.String(), float64(balance.Int64())/1e6)
	return balance
}

// shouldUsePaymasterAutoDecision determines if paymaster should be used based on USDC balance
func shouldUsePaymasterAutoDecision(t *testing.T, setup *uniswapSwapTestSetup) bool {
	// Check if paymaster is configured
	if setup.cfg.SmartWallet.PaymasterAddress == (common.Address{}) {
		t.Logf("💳 No paymaster configured - must self-fund")
		return false
	}

	// Check USDC balance
	usdcBalance := checkUSDCBalance(t, setup)
	requiredAmount := big.NewInt(10000) // 0.01 USDC (6 decimals)

	if usdcBalance.Cmp(requiredAmount) < 0 {
		t.Logf("💳 Insufficient USDC balance (%.6f < 0.01) - using paymaster", float64(usdcBalance.Int64())/1e6)
		return true
	}

	t.Logf("💰 Sufficient USDC balance (%.6f >= 0.01) - self-funding", float64(usdcBalance.Int64())/1e6)
	return false
}

// TestRunNodeImmediately_UniswapSwap_Base tests the full approve + swap flow on Base mainnet
func TestRunNodeImmediately_UniswapSwap_Base(t *testing.T) {
	setup := setupUniswapSwapTest(t)
	defer setup.client.Close()

	t.Logf("🚀 Starting Uniswap swap test with auto-decision logic...")

	// Step 0: Check current allowance using contract read
	t.Logf("🔍 Step 0: Checking current USDC allowance...")
	currentAllowance := checkUSDCAllowance(t, setup)
	requiredAmount := big.NewInt(10000) // 0.01 USDC (6 decimals)

	// Step 1: Auto-decision for paymaster vs self-fund
	t.Logf("🤖 Step 1: Auto-deciding paymaster vs self-fund...")
	usePaymaster := shouldUsePaymasterAutoDecision(t, setup)

	// Common settings for all nodes
	settings := map[string]interface{}{
		"runner":   setup.smartWalletAddr.Hex(),
		"chain_id": setup.chain.chainID,
		"amount":   setup.chain.swapAmount,
	}

	inputVars := map[string]interface{}{
		"settings": settings,
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

	// Step 2: Approve USDC to SwapRouter (only if needed)
	if currentAllowance.Cmp(requiredAmount) < 0 {
		t.Logf("✍️  Step 2: Approving USDC to SwapRouter (current: %.6f, required: 0.01)...", float64(currentAllowance.Int64())/1e6)

		approvalConfig := map[string]interface{}{
			"contractAddress": setup.chain.usdc,
			"contractAbi":     erc20ABI,
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName":   "approve",
					"methodParams": []interface{}{setup.chain.swapRouter, setup.chain.swapAmount},
					"callData":     "",
				},
			},
		}

		t.Logf("   Calling RunNodeImmediately with isSimulated=false...")
		approvalResult, err := setup.engine.RunNodeImmediately("contractWrite", approvalConfig, inputVars, setup.user, false, &usePaymaster)

		require.NoError(t, err, "Approval RunNodeImmediately should not return error")
		require.NotNil(t, approvalResult, "Approval result should not be nil")

		// Check if the execution succeeded
		if successVal, ok := approvalResult["success"]; ok {
			if success, isBool := successVal.(bool); isBool && !success {
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
		t.Logf("⏳ Waiting 5 seconds for approval to propagate across RPC nodes...")
		time.Sleep(5 * time.Second)
	} else {
		t.Logf("⏭️  Step 2: Skipping approval - sufficient allowance already exists (%.6f >= 0.01)", float64(currentAllowance.Int64())/1e6)
	}

	// Step 3: Verify on-chain state before swap (diagnostic)
	t.Logf("🔍 Step 3: Verifying on-chain state before swap...")

	// Initialize USDC contract for verification
	usdc, err := erc20.NewErc20(common.HexToAddress(setup.chain.usdc), setup.client)
	require.NoError(t, err, "Failed to create USDC contract binding")

	// 1) Balance check
	bal, err := usdc.BalanceOf(nil, *setup.smartWalletAddr)
	require.NoError(t, err)
	t.Logf("🔎 USDC.balanceOf(wallet) = %s", bal.String())

	// 2) Allowance to the router
	router := common.HexToAddress(setup.chain.swapRouter)
	allow, err := usdc.Allowance(nil, *setup.smartWalletAddr, router)
	require.NoError(t, err)
	t.Logf("🔎 USDC.allowance(wallet, router=%s) = %s", router.Hex(), allow.String())

	// 3) (Optional) Allowance to Permit2 (in case router is using it)
	permit2 := common.HexToAddress("0x000000000022D473030F116dDEE9F6B43aC78BA3") // same on Base
	allowP2, err := usdc.Allowance(nil, *setup.smartWalletAddr, permit2)
	require.NoError(t, err)
	t.Logf("🔎 USDC.allowance(wallet, permit2=%s) = %s", permit2.Hex(), allowP2.String())

	// Expected for success:
	// balanceOf >= 10000 (you have 9,990,000, good)
	// allowance (wallet, router) >= 10000 (this is the one I expect to be 0 in your failing run)
	// If allowance == 0, the approve didn't apply to the exact (owner=wallet, spender=router) pair that the swap path uses.

	requiredAmountForSwap := big.NewInt(10000) // 0.01 USDC (6 decimals)
	if allow.Cmp(requiredAmountForSwap) < 0 {
		t.Logf("❌ ALLOWANCE ISSUE DETECTED: allowance(wallet, router) = %s < %s", allow.String(), requiredAmountForSwap.String())
		t.Logf("   This explains the TRANSFER_FROM_FAILED error!")
		t.Logf("   The approve transaction succeeded but didn't set allowance for the correct spender.")
	} else {
		t.Logf("✅ Allowance is sufficient: %s >= %s", allow.String(), requiredAmountForSwap.String())
		t.Logf("   The TRANSFER_FROM_FAILED error must be from a different cause.")
	}

	// Step 4: Get quote from QuoterV2 for proper amountOutMinimum
	t.Logf("📊 Step 4: Getting quote from QuoterV2...")

	// QuoterV2 ABI for quoteExactInputSingle
	// Note: QuoterV2 returns 4 values, not just amountOut
	quoterV2ABI := []interface{}{
		map[string]interface{}{
			"inputs": []interface{}{
				map[string]interface{}{
					"components": []interface{}{
						map[string]interface{}{"name": "tokenIn", "type": "address"},
						map[string]interface{}{"name": "tokenOut", "type": "address"},
						map[string]interface{}{"name": "amountIn", "type": "uint256"},
						map[string]interface{}{"name": "fee", "type": "uint24"},
						map[string]interface{}{"name": "sqrtPriceLimitX96", "type": "uint160"},
					},
					"name": "params",
					"type": "tuple",
				},
			},
			"name": "quoteExactInputSingle",
			"outputs": []interface{}{
				map[string]interface{}{"name": "amountOut", "type": "uint256"},
				map[string]interface{}{"name": "sqrtPriceX96After", "type": "uint160"},
				map[string]interface{}{"name": "initializedTicksCrossed", "type": "uint32"},
				map[string]interface{}{"name": "gasEstimate", "type": "uint256"},
			},
			"stateMutability": "nonpayable",
			"type":            "function",
		},
	}

	quoteConfig := map[string]interface{}{
		"contractAddress": setup.chain.quoter,
		"contractAbi":     quoterV2ABI,
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName": "quoteExactInputSingle",
				"methodParams": []interface{}{
					// Tuple parameter: (tokenIn, tokenOut, amountIn, fee, sqrtPriceLimitX96)
					// Note: Parameter order matters! amountIn comes BEFORE fee in QuoterV2
					// Note: Numbers should NOT be quoted in the JSON array
					fmt.Sprintf(`["%s","%s",%s,%d,0]`,
						setup.chain.usdc,       // tokenIn
						setup.chain.weth,       // tokenOut
						setup.chain.swapAmount, // amountIn (NO quotes)
						setup.chain.feeTier,    // fee
					),
				},
				"callData": "",
			},
		},
	}

	t.Logf("   Getting quote for %s USDC → WETH...", setup.chain.swapAmount)
	quoteResult, err := setup.engine.RunNodeImmediately("contractRead", quoteConfig, inputVars, setup.user, true) // simulation mode

	require.NoError(t, err, "Quote RunNodeImmediately should not return error")
	require.NotNil(t, quoteResult, "Quote result should not be nil")

	// Extract the quoted amount
	// The contract read result structure is: {"data": {"methodName": {"output1": value1, ...}}}
	var quotedAmount string
	if data, ok := quoteResult["data"].(map[string]interface{}); ok {
		if quoteData, hasQuoteData := data["quoteExactInputSingle"].(map[string]interface{}); hasQuoteData {
			if amountOut, hasAmountOut := quoteData["amountOut"]; hasAmountOut {
				// amountOut might be int64, float64, or string depending on how it was decoded
				switch v := amountOut.(type) {
				case string:
					quotedAmount = v
				case int64:
					quotedAmount = fmt.Sprintf("%d", v)
				case float64:
					quotedAmount = fmt.Sprintf("%.0f", v)
				default:
					quotedAmount = fmt.Sprintf("%v", v)
				}
			}
		}
	}

	require.NotEmpty(t, quotedAmount, "Should get quoted amount from QuoterV2")

	// Calculate amountOutMinimum with 5% slippage tolerance
	quotedAmountBig, ok := new(big.Int).SetString(quotedAmount, 10)
	require.True(t, ok, "Should parse quoted amount as big.Int")

	// Apply 5% slippage tolerance (multiply by 0.95)
	slippageTolerance := big.NewInt(95) // 95% of quoted amount
	slippageDenominator := big.NewInt(100)
	minOutputBig := new(big.Int).Mul(quotedAmountBig, slippageTolerance)
	minOutputBig.Div(minOutputBig, slippageDenominator)

	minOutput := minOutputBig.String()

	t.Logf("✅ Quote received:")
	t.Logf("   Quoted amount: %s wei", quotedAmount)
	t.Logf("   Min output (5%% slippage): %s wei", minOutput)
	t.Logf("   Slippage tolerance: 5%%")

	// Step 5: Execute Swap
	t.Logf("🔄 Step 5: Executing USDC → WETH swap...")

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
		"contractAddress": setup.chain.swapRouter,
		"contractAbi":     swapRouterABI,
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName": "exactInputSingle",
				"methodParams": []interface{}{
					fmt.Sprintf(`["%s", "%s", "%d", "%s", "%s", "%s", 0]`,
						setup.chain.usdc,
						setup.chain.weth,
						setup.chain.feeTier,
						setup.smartWalletAddr.Hex(),
						setup.chain.swapAmount,
						minOutput,
					),
				},
				"callData": "",
			},
		},
	}

	t.Logf("   Swap parameters:")
	t.Logf("     tokenIn: %s (USDC)", setup.chain.usdc)
	t.Logf("     tokenOut: %s (WETH)", setup.chain.weth)
	t.Logf("     fee: %d", setup.chain.feeTier)
	t.Logf("     amountIn: %s (0.01 USDC)", setup.chain.swapAmount)
	t.Logf("     amountOutMinimum: %s", minOutput)
	t.Logf("     funding: %s", map[bool]string{true: "paymaster-sponsored", false: "self-funded"}[usePaymaster])

	t.Logf("   Calling RunNodeImmediately with isSimulated=false...")
	swapResult, err := setup.engine.RunNodeImmediately("contractWrite", swapConfig, inputVars, setup.user, false, &usePaymaster)

	require.NoError(t, err, "Swap RunNodeImmediately should not return error")
	require.NotNil(t, swapResult, "Swap result should not be nil")

	// Check if the execution succeeded
	if successVal, ok := swapResult["success"]; ok {
		if success, isBool := successVal.(bool); isBool && !success {
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

	t.Logf("🎉 SUCCESS: Uniswap swap completed successfully on Base with auto-decision logic!")
}

// TestRunNodeImmediately_UniswapSwap_Base_SelfFunded tests the swap flow with forced self-funding
func TestRunNodeImmediately_UniswapSwap_Base_SelfFunded(t *testing.T) {
	setup := setupUniswapSwapTest(t)
	defer setup.client.Close()

	t.Logf("🚀 Starting Uniswap swap test with FORCED self-funding...")

	// Step 0: Check current allowance using contract read
	t.Logf("🔍 Step 0: Checking current USDC allowance...")
	currentAllowance := checkUSDCAllowance(t, setup)
	requiredAmount := big.NewInt(10000) // 0.01 USDC (6 decimals)

	// Step 1: Force self-funding (no paymaster)
	t.Logf("💰 Step 1: Forcing self-funding (no paymaster override)...")
	usePaymaster := false

	// Check USDC balance to ensure we can self-fund
	usdcBalance := checkUSDCBalance(t, setup)
	require.True(t, usdcBalance.Cmp(requiredAmount) >= 0,
		"wallet %s needs at least 0.01 USDC (10000 raw) for self-funded swap; have %s",
		setup.smartWalletAddr.Hex(), usdcBalance.String())

	// Common settings for all nodes
	settings := map[string]interface{}{
		"runner":   setup.smartWalletAddr.Hex(),
		"chain_id": setup.chain.chainID,
		"amount":   setup.chain.swapAmount,
	}

	inputVars := map[string]interface{}{
		"settings": settings,
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

	// Step 2: Approve USDC to SwapRouter (only if needed)
	if currentAllowance.Cmp(requiredAmount) < 0 {
		t.Logf("✍️  Step 2: Approving USDC to SwapRouter (current: %.6f, required: 0.01)...", float64(currentAllowance.Int64())/1e6)

		approvalConfig := map[string]interface{}{
			"contractAddress": setup.chain.usdc,
			"contractAbi":     erc20ABI,
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName":   "approve",
					"methodParams": []interface{}{setup.chain.swapRouter, setup.chain.swapAmount},
					"callData":     "",
				},
			},
		}

		t.Logf("   Calling RunNodeImmediately with isSimulated=false (self-funded)...")
		approvalResult, err := setup.engine.RunNodeImmediately("contractWrite", approvalConfig, inputVars, setup.user, false, &usePaymaster)

		require.NoError(t, err, "Approval RunNodeImmediately should not return error")
		require.NotNil(t, approvalResult, "Approval result should not be nil")

		// Check if the execution succeeded
		if successVal, ok := approvalResult["success"]; ok {
			if success, isBool := successVal.(bool); isBool && !success {
				if errorVal, hasError := approvalResult["error"]; hasError {
					if errorStr, isString := errorVal.(string); isString {
						t.Fatalf("❌ Approval execution failed: %s", errorStr)
					}
				}
				t.Fatalf("❌ Approval execution failed with success=false")
			}
		}

		t.Logf("✅ Approval confirmed on-chain (self-funded)")
		t.Logf("   Result: %+v", approvalResult)

		// Wait 5 seconds to ensure approval is fully propagated across RPC nodes
		t.Logf("⏳ Waiting 5 seconds for approval to propagate across RPC nodes...")
		time.Sleep(5 * time.Second)
	} else {
		t.Logf("⏭️  Step 2: Skipping approval - sufficient allowance already exists (%.6f >= 0.01)", float64(currentAllowance.Int64())/1e6)
	}

	// Step 3: Verify on-chain state before swap (diagnostic)
	t.Logf("🔍 Step 3: Verifying on-chain state before swap...")

	// Initialize USDC contract for verification
	usdc, err := erc20.NewErc20(common.HexToAddress(setup.chain.usdc), setup.client)
	require.NoError(t, err, "Failed to create USDC contract binding")

	// 1) Balance check
	bal, err := usdc.BalanceOf(nil, *setup.smartWalletAddr)
	require.NoError(t, err)
	t.Logf("🔎 USDC.balanceOf(wallet) = %s", bal.String())

	// 2) Allowance to the router
	router := common.HexToAddress(setup.chain.swapRouter)
	allow, err := usdc.Allowance(nil, *setup.smartWalletAddr, router)
	require.NoError(t, err)
	t.Logf("🔎 USDC.allowance(wallet, router=%s) = %s", router.Hex(), allow.String())

	// 3) (Optional) Allowance to Permit2 (in case router is using it)
	permit2 := common.HexToAddress("0x000000000022D473030F116dDEE9F6B43aC78BA3") // same on Base
	allowP2, err := usdc.Allowance(nil, *setup.smartWalletAddr, permit2)
	require.NoError(t, err)
	t.Logf("🔎 USDC.allowance(wallet, permit2=%s) = %s", permit2.Hex(), allowP2.String())

	// Expected for success:
	// balanceOf >= 10000 (you have 9,990,000, good)
	// allowance (wallet, router) >= 10000 (this is the one I expect to be 0 in your failing run)
	// If allowance == 0, the approve didn't apply to the exact (owner=wallet, spender=router) pair that the swap path uses.

	requiredAmountForSwap := big.NewInt(10000) // 0.01 USDC (6 decimals)
	if allow.Cmp(requiredAmountForSwap) < 0 {
		t.Logf("❌ ALLOWANCE ISSUE DETECTED: allowance(wallet, router) = %s < %s", allow.String(), requiredAmountForSwap.String())
		t.Logf("   This explains the TRANSFER_FROM_FAILED error!")
		t.Logf("   The approve transaction succeeded but didn't set allowance for the correct spender.")
	} else {
		t.Logf("✅ Allowance is sufficient: %s >= %s", allow.String(), requiredAmountForSwap.String())
		t.Logf("   The TRANSFER_FROM_FAILED error must be from a different cause.")
	}

	// Step 4: Execute Swap (self-funded)
	t.Logf("🔄 Step 4: Executing USDC → WETH swap (self-funded)...")

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
		"contractAddress": setup.chain.swapRouter,
		"contractAbi":     swapRouterABI,
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName": "exactInputSingle",
				"methodParams": []interface{}{
					fmt.Sprintf(`["%s", "%s", "%d", "%s", "%s", "%s", 0]`,
						setup.chain.usdc,
						setup.chain.weth,
						setup.chain.feeTier,
						setup.smartWalletAddr.Hex(),
						setup.chain.swapAmount,
						minOutput,
					),
				},
				"callData": "",
			},
		},
	}

	t.Logf("   Swap parameters:")
	t.Logf("     tokenIn: %s (USDC)", setup.chain.usdc)
	t.Logf("     tokenOut: %s (WETH)", setup.chain.weth)
	t.Logf("     fee: %d", setup.chain.feeTier)
	t.Logf("     amountIn: %s (0.01 USDC)", setup.chain.swapAmount)
	t.Logf("     amountOutMinimum: %s", minOutput)
	t.Logf("     funding: self-funded (no paymaster)")

	t.Logf("   Calling RunNodeImmediately with isSimulated=false (self-funded)...")
	swapResult, err := setup.engine.RunNodeImmediately("contractWrite", swapConfig, inputVars, setup.user, false, &usePaymaster)

	require.NoError(t, err, "Swap RunNodeImmediately should not return error")
	require.NotNil(t, swapResult, "Swap result should not be nil")

	// Check if the execution succeeded
	if successVal, ok := swapResult["success"]; ok {
		if success, isBool := successVal.(bool); isBool && !success {
			if errorVal, hasError := swapResult["error"]; hasError {
				if errorStr, isString := errorVal.(string); isString {
					t.Fatalf("❌ Swap execution failed: %s", errorStr)
				}
			}
			t.Fatalf("❌ Swap execution failed with success=false")
		}
	}

	t.Logf("✅ Swap confirmed on-chain (self-funded)")
	t.Logf("   Result: %+v", swapResult)

	// Extract transaction hash
	if data, ok := swapResult["data"].(map[string]interface{}); ok {
		t.Logf("   Swap data: %+v", data)
	}

	t.Logf("🎉 SUCCESS: Self-funded Uniswap swap completed successfully on Base!")
}

// TestRunNodeImmediately_UniswapSwap_Base_WithQuoter tests the swap with proper QuoterV2 quote
func TestRunNodeImmediately_UniswapSwap_Base_WithQuoter(t *testing.T) {
	setup := setupUniswapSwapTest(t)
	defer setup.client.Close()

	t.Logf("🚀 Starting Uniswap swap test with QuoterV2 quote...")

	// Step 0: Check current allowance using contract read
	t.Logf("🔍 Step 0: Checking current USDC allowance...")
	currentAllowance := checkUSDCAllowance(t, setup)
	requiredAmount := big.NewInt(10000) // 0.01 USDC (6 decimals)

	// Step 1: Auto-decision for paymaster vs self-fund
	t.Logf("🤖 Step 1: Auto-deciding paymaster vs self-fund...")
	usePaymaster := shouldUsePaymasterAutoDecision(t, setup)

	// Common settings for all nodes
	settings := map[string]interface{}{
		"runner":   setup.smartWalletAddr.Hex(),
		"chain_id": setup.chain.chainID,
		"amount":   setup.chain.swapAmount,
	}

	inputVars := map[string]interface{}{
		"settings": settings,
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

	// Step 2: Approve USDC to SwapRouter (only if needed)
	if currentAllowance.Cmp(requiredAmount) < 0 {
		t.Logf("✍️  Step 2: Approving USDC to SwapRouter (current: %.6f, required: 0.01)...", float64(currentAllowance.Int64())/1e6)

		approvalConfig := map[string]interface{}{
			"contractAddress": setup.chain.usdc,
			"contractAbi":     erc20ABI,
			"methodCalls": []interface{}{
				map[string]interface{}{
					"methodName":   "approve",
					"methodParams": []interface{}{setup.chain.swapRouter, setup.chain.swapAmount},
					"callData":     "",
				},
			},
		}

		t.Logf("   Calling RunNodeImmediately with isSimulated=false...")
		approvalResult, err := setup.engine.RunNodeImmediately("contractWrite", approvalConfig, inputVars, setup.user, false, &usePaymaster)

		require.NoError(t, err, "Approval RunNodeImmediately should not return error")
		require.NotNil(t, approvalResult, "Approval result should not be nil")

		// Check if the execution succeeded
		if successVal, ok := approvalResult["success"]; ok {
			if success, isBool := successVal.(bool); isBool && !success {
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
		t.Logf("⏳ Waiting 5 seconds for approval to propagate across RPC nodes...")
		time.Sleep(5 * time.Second)
	} else {
		t.Logf("⏭️  Step 2: Skipping approval - sufficient allowance already exists (%.6f >= 0.01)", float64(currentAllowance.Int64())/1e6)
	}

	// Step 3: Get quote from QuoterV2 for proper amountOutMinimum
	t.Logf("📊 Step 3: Getting quote from QuoterV2...")

	// QuoterV2 ABI for quoteExactInputSingle
	// Note: QuoterV2 returns 4 values, not just amountOut
	quoterV2ABI := []interface{}{
		map[string]interface{}{
			"inputs": []interface{}{
				map[string]interface{}{
					"components": []interface{}{
						map[string]interface{}{"name": "tokenIn", "type": "address"},
						map[string]interface{}{"name": "tokenOut", "type": "address"},
						map[string]interface{}{"name": "amountIn", "type": "uint256"},
						map[string]interface{}{"name": "fee", "type": "uint24"},
						map[string]interface{}{"name": "sqrtPriceLimitX96", "type": "uint160"},
					},
					"name": "params",
					"type": "tuple",
				},
			},
			"name": "quoteExactInputSingle",
			"outputs": []interface{}{
				map[string]interface{}{"name": "amountOut", "type": "uint256"},
				map[string]interface{}{"name": "sqrtPriceX96After", "type": "uint160"},
				map[string]interface{}{"name": "initializedTicksCrossed", "type": "uint32"},
				map[string]interface{}{"name": "gasEstimate", "type": "uint256"},
			},
			"stateMutability": "nonpayable",
			"type":            "function",
		},
	}

	quoteConfig := map[string]interface{}{
		"contractAddress": setup.chain.quoter,
		"contractAbi":     quoterV2ABI,
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName": "quoteExactInputSingle",
				"methodParams": []interface{}{
					// Tuple parameter: (tokenIn, tokenOut, amountIn, fee, sqrtPriceLimitX96)
					// Note: Parameter order matters! amountIn comes BEFORE fee in QuoterV2
					fmt.Sprintf(`["%s", "%s", %s, %d, 0]`,
						setup.chain.usdc,       // tokenIn
						setup.chain.weth,       // tokenOut
						setup.chain.swapAmount, // amountIn (no quotes)
						setup.chain.feeTier,    // fee
					),
				},
				"callData": "",
			},
		},
	}

	t.Logf("   Getting quote for %s USDC → WETH...", setup.chain.swapAmount)
	quoteResult, err := setup.engine.RunNodeImmediately("contractRead", quoteConfig, inputVars, setup.user, true) // simulation mode

	require.NoError(t, err, "Quote RunNodeImmediately should not return error")
	require.NotNil(t, quoteResult, "Quote result should not be nil")

	t.Logf("🔍 Quote result structure: %+v", quoteResult)

	// Extract the quoted amount
	// The contract read result structure is: {"data": {"methodName": {"output1": value1, ...}}}
	var quotedAmount string
	if data, ok := quoteResult["data"].(map[string]interface{}); ok {
		t.Logf("🔍 Quote data: %+v", data)
		if quoteData, hasQuoteData := data["quoteExactInputSingle"].(map[string]interface{}); hasQuoteData {
			t.Logf("🔍 Quote data for quoteExactInputSingle: %+v", quoteData)
			if amountOut, hasAmountOut := quoteData["amountOut"]; hasAmountOut {
				t.Logf("🔍 Quote amountOut: %+v", amountOut)
				// amountOut might be int64, float64, or string depending on how it was decoded
				switch v := amountOut.(type) {
				case string:
					quotedAmount = v
				case int64:
					quotedAmount = fmt.Sprintf("%d", v)
				case float64:
					quotedAmount = fmt.Sprintf("%.0f", v)
				default:
					quotedAmount = fmt.Sprintf("%v", v)
				}
			}
		}
	}

	if quotedAmount == "" {
		t.Logf("❌ Failed to extract quoted amount from result structure")
		t.Logf("   Full quote result: %+v", quoteResult)
	}

	require.NotEmpty(t, quotedAmount, "Should get quoted amount from QuoterV2")

	// Calculate amountOutMinimum with 5% slippage tolerance
	quotedAmountBig, ok := new(big.Int).SetString(quotedAmount, 10)
	require.True(t, ok, "Should parse quoted amount as big.Int")

	// Apply 5% slippage tolerance (multiply by 0.95)
	slippageTolerance := big.NewInt(95) // 95% of quoted amount
	slippageDenominator := big.NewInt(100)
	minOutputBig := new(big.Int).Mul(quotedAmountBig, slippageTolerance)
	minOutputBig.Div(minOutputBig, slippageDenominator)

	minOutput := minOutputBig.String()

	t.Logf("✅ Quote received:")
	t.Logf("   Quoted amount: %s wei", quotedAmount)
	t.Logf("   Min output (5%% slippage): %s wei", minOutput)
	t.Logf("   Slippage tolerance: 5%%")

	// Step 4: Execute Swap with proper amountOutMinimum
	t.Logf("🔄 Step 4: Executing USDC → WETH swap with QuoterV2-based minimum...")

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
		"contractAddress": setup.chain.swapRouter,
		"contractAbi":     swapRouterABI,
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName": "exactInputSingle",
				"methodParams": []interface{}{
					fmt.Sprintf(`["%s", "%s", "%d", "%s", "%s", "%s", 0]`,
						BASE_USDC,
						BASE_WETH,
						BASE_FEE_TIER,
						setup.smartWalletAddr.Hex(),
						BASE_SWAP_AMOUNT,
						minOutput, // Use QuoterV2-based minimum
					),
				},
				"callData": "",
			},
		},
	}

	t.Logf("   Swap parameters:")
	t.Logf("     tokenIn: %s (USDC)", setup.chain.usdc)
	t.Logf("     tokenOut: %s (WETH)", setup.chain.weth)
	t.Logf("     fee: %d", setup.chain.feeTier)
	t.Logf("     amountIn: %s (0.01 USDC)", setup.chain.swapAmount)
	t.Logf("     amountOutMinimum: %s (from QuoterV2)", minOutput)
	t.Logf("     funding: %s", map[bool]string{true: "paymaster-sponsored", false: "self-funded"}[usePaymaster])

	t.Logf("   Calling RunNodeImmediately with isSimulated=false...")
	swapResult, err := setup.engine.RunNodeImmediately("contractWrite", swapConfig, inputVars, setup.user, false, &usePaymaster)

	require.NoError(t, err, "Swap RunNodeImmediately should not return error")
	require.NotNil(t, swapResult, "Swap result should not be nil")

	// Check if the execution succeeded
	if successVal, ok := swapResult["success"]; ok {
		if success, isBool := successVal.(bool); isBool && !success {
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

	t.Logf("🎉 SUCCESS: Uniswap swap with QuoterV2 completed successfully on Base!")
}

// TestRunNodeImmediately_UniswapSwap_Base_CheckSpender tests the swap with explicit spender verification
func TestRunNodeImmediately_UniswapSwap_Base_CheckSpender(t *testing.T) {
	setup := setupUniswapSwapTest(t)
	defer setup.client.Close()

	t.Logf("🚀 Starting Uniswap swap test with explicit spender verification...")

	// Step 0: Check current allowance using contract read
	t.Logf("🔍 Step 0: Checking current USDC allowance...")
	_ = checkUSDCAllowance(t, setup)    // We'll always do belt-and-suspenders regardless
	requiredAmount := big.NewInt(10000) // 0.01 USDC (6 decimals)

	// Step 1: Auto-decision for paymaster vs self-fund
	t.Logf("🤖 Step 1: Auto-deciding paymaster vs self-fund...")
	usePaymaster := shouldUsePaymasterAutoDecision(t, setup)

	// Common settings for all nodes
	settings := map[string]interface{}{
		"runner":   setup.smartWalletAddr.Hex(),
		"chain_id": setup.chain.chainID,
		"amount":   setup.chain.swapAmount,
	}

	inputVars := map[string]interface{}{
		"settings": settings,
	}

	// Step 2: Explicit spender verification and approval
	t.Logf("🔍 Step 2: Verifying spender addresses...")

	// Initialize USDC contract for verification
	usdc, err := erc20.NewErc20(common.HexToAddress(setup.chain.usdc), setup.client)
	require.NoError(t, err, "Failed to create USDC contract binding")

	router := common.HexToAddress(setup.chain.swapRouter)
	permit2 := common.HexToAddress("0x000000000022D473030F116dDEE9F6B43aC78BA3")

	bal, err := usdc.BalanceOf(nil, *setup.smartWalletAddr)
	require.NoError(t, err)
	t.Logf("🔎 USDC.balanceOf(wallet) = %s", bal.String())

	allowR, err := usdc.Allowance(nil, *setup.smartWalletAddr, router)
	require.NoError(t, err)
	t.Logf("🔎 USDC.allowance(wallet, router=%s) = %s", router.Hex(), allowR.String())

	allowP, err := usdc.Allowance(nil, *setup.smartWalletAddr, permit2)
	require.NoError(t, err)
	t.Logf("🔎 USDC.allowance(wallet, permit2=%s) = %s", permit2.Hex(), allowP.String())

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

	// Step 3: Try belt-and-suspenders approve(0) then approve(10000) approach
	t.Logf("✍️  Step 3: Trying belt-and-suspenders approve(0) then approve(10000)...")
	t.Logf("   Approving to router: %s", router.Hex())

	// First: approve(0) to reset allowance
	t.Logf("   Step 3a: Approving 0 to reset allowance...")
	approvalConfig0 := map[string]interface{}{
		"contractAddress": setup.chain.usdc,
		"contractAbi":     erc20ABI,
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName":   "approve",
				"methodParams": []interface{}{router.Hex(), "0"}, // Reset to 0
				"callData":     "",
			},
		},
	}

	approvalResult0, err := setup.engine.RunNodeImmediately("contractWrite", approvalConfig0, inputVars, setup.user, false, &usePaymaster)
	require.NoError(t, err, "Approval(0) RunNodeImmediately should not return error")
	require.NotNil(t, approvalResult0, "Approval(0) result should not be nil")

	// Check if the execution succeeded
	if successVal, ok := approvalResult0["success"]; ok {
		if success, isBool := successVal.(bool); isBool && !success {
			if errorVal, hasError := approvalResult0["error"]; hasError {
				if errorStr, isString := errorVal.(string); isString {
					t.Fatalf("❌ Approval(0) execution failed: %s", errorStr)
				}
			}
			t.Fatalf("❌ Approval(0) execution failed with success=false")
		}
	}

	t.Logf("✅ Approval(0) confirmed on-chain")

	// Wait 2 seconds between approvals
	time.Sleep(2 * time.Second)

	// Second: approve(10000) to set proper allowance
	t.Logf("   Step 3b: Approving %s to set proper allowance...", BASE_SWAP_AMOUNT)
	approvalConfig := map[string]interface{}{
		"contractAddress": setup.chain.usdc,
		"contractAbi":     erc20ABI,
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName":   "approve",
				"methodParams": []interface{}{router.Hex(), BASE_SWAP_AMOUNT}, // Set to required amount
				"callData":     "",
			},
		},
	}

	approvalResult, err := setup.engine.RunNodeImmediately("contractWrite", approvalConfig, inputVars, setup.user, false, &usePaymaster)
	require.NoError(t, err, "Approval RunNodeImmediately should not return error")
	require.NotNil(t, approvalResult, "Approval result should not be nil")

	// Check if the execution succeeded
	if successVal, ok := approvalResult["success"]; ok {
		if success, isBool := successVal.(bool); isBool && !success {
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
	t.Logf("⏳ Waiting 5 seconds for approval to propagate across RPC nodes...")
	time.Sleep(5 * time.Second)

	// Verify the approval was set correctly
	newAllowR, err := usdc.Allowance(nil, *setup.smartWalletAddr, router)
	require.NoError(t, err)
	t.Logf("🔎 USDC.allowance(wallet, router) after belt-and-suspenders approval = %s", newAllowR.String())

	require.True(t, newAllowR.Cmp(requiredAmount) >= 0, "Allowance should be >= required amount after approval")

	// Step 4: Diagnostic checks (4 checks - fast, decisive)
	t.Logf("🔍 Step 4: Running diagnostic checks right before building the failing swap UO...")

	// Define addresses for diagnostic checks
	wallet := *setup.smartWalletAddr

	// Perform the 4 diagnostic checks
	bal, err = usdc.BalanceOf(nil, wallet)
	require.NoError(t, err, "Failed to get USDC balance")

	allowR, err = usdc.Allowance(nil, wallet, router)
	require.NoError(t, err, "Failed to get allowance to router")

	allowP, err = usdc.Allowance(nil, wallet, permit2)
	require.NoError(t, err, "Failed to get allowance to permit2")

	// Log the diagnostic results
	t.Logf("🔎 USDC.balanceOf(wallet) = %s", bal.String())
	t.Logf("🔎 USDC.allowance(wallet, router) = %s router=%s", allowR.String(), router.Hex())
	t.Logf("🔎 USDC.allowance(wallet, permit2) = %s", allowP.String())

	// Interpretation based on the diagnostic results
	requiredAmountForDiagnostic := big.NewInt(10000) // 0.01 USDC (6 decimals)
	if allowR.Cmp(requiredAmountForDiagnostic) < 0 {
		t.Logf("❌ DIAGNOSTIC RESULT: allowance(wallet, router) = %s < %s", allowR.String(), requiredAmountForDiagnostic.String())
		t.Logf("   → Your approve didn't actually target the same (token, owner, spender, chain) the swap uses")
		t.Logf("   → Fix that and it will pass")
		t.Fatalf("Allowance insufficient: %s < %s", allowR.String(), requiredAmountForDiagnostic.String())
	} else {
		t.Logf("✅ DIAGNOSTIC RESULT: allowance(wallet, router) = %s >= %s", allowR.String(), requiredAmountForDiagnostic.String())
		t.Logf("   → Allowance is sufficient, the STF error must be from a different cause")
		t.Logf("   → This suggests the issue is with gas estimation, not allowance")
	}

	// Step 5: Execute Swap with realistic amountOutMinimum
	t.Logf("🔄 Step 5: Executing USDC → WETH swap with realistic minimum...")

	// Use a more realistic minimum output (roughly 0.00001 ETH for 0.01 USDC)
	// This is much more realistic than 1 wei
	minOutput := "10000000000000" // 0.00001 ETH (10,000,000,000,000 wei)

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
		"contractAddress": setup.chain.swapRouter,
		"contractAbi":     swapRouterABI,
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName": "exactInputSingle",
				"methodParams": []interface{}{
					fmt.Sprintf(`["%s", "%s", "%d", "%s", "%s", "%s", 0]`,
						BASE_USDC,
						BASE_WETH,
						BASE_FEE_TIER,
						setup.smartWalletAddr.Hex(),
						BASE_SWAP_AMOUNT,
						minOutput, // Use realistic minimum
					),
				},
				"callData": "",
			},
		},
	}

	t.Logf("   Swap parameters:")
	t.Logf("     tokenIn: %s (USDC)", setup.chain.usdc)
	t.Logf("     tokenOut: %s (WETH)", setup.chain.weth)
	t.Logf("     fee: %d", setup.chain.feeTier)
	t.Logf("     amountIn: %s (0.01 USDC)", setup.chain.swapAmount)
	t.Logf("     amountOutMinimum: %s (realistic minimum)", minOutput)
	t.Logf("     funding: %s", map[bool]string{true: "paymaster-sponsored", false: "self-funded"}[usePaymaster])

	t.Logf("   Calling RunNodeImmediately with isSimulated=false...")
	swapResult, err := setup.engine.RunNodeImmediately("contractWrite", swapConfig, inputVars, setup.user, false, &usePaymaster)

	require.NoError(t, err, "Swap RunNodeImmediately should not return error")
	require.NotNil(t, swapResult, "Swap result should not be nil")

	// Check if the execution succeeded
	if successVal, ok := swapResult["success"]; ok {
		if success, isBool := successVal.(bool); isBool && !success {
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

	t.Logf("🎉 SUCCESS: Uniswap swap with spender verification completed successfully on Base!")
}
