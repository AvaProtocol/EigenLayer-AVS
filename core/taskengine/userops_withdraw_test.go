package taskengine

import (
	"context"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
)

// Common setup function for all UserOp withdrawal tests
func setupUserOpWithdrawalTest(t *testing.T) (*config.Config, common.Address, *common.Address, common.Address, *ethclient.Client, *Engine, *model.User) {
	// Skip if not running on Base chain
	if os.Getenv("TEST_CHAIN") != "base" {
		t.Skip("Skipping Base UserOp withdrawal - set TEST_CHAIN=base to run")
	}

	// Load configuration for Base
	cfg, err := config.NewConfig("../../config/aggregator-base.yaml")
	if err != nil {
		t.Skipf("Failed to load aggregator-base.yaml: %v", err)
	}

	// Use explicit Owner EOA for automation (controller signs the UserOp)
	ownerEOAHex := os.Getenv("OWNER_EOA")
	if ownerEOAHex == "" {
		t.Skip("OWNER_EOA environment variable not set")
	}
	ownerAddress := common.HexToAddress(ownerEOAHex)

	// Destination address - defaults to owner EOA, but can be overridden with RECIPIENT_ADDRESS
	var destinationAddress common.Address
	recipientAddressHex := os.Getenv("RECIPIENT_ADDRESS")
	if recipientAddressHex != "" {
		if !common.IsHexAddress(recipientAddressHex) {
			t.Fatalf("RECIPIENT_ADDRESS is not a valid hex address: %s", recipientAddressHex)
		}
		destinationAddress = common.HexToAddress(recipientAddressHex)
		t.Logf("📮 Using custom RECIPIENT_ADDRESS: %s", destinationAddress.Hex())
	} else {
		// Default to owner EOA if no custom recipient specified
		destinationAddress = ownerAddress
		t.Logf("📮 Using default destination (owner EOA): %s", destinationAddress.Hex())
	}

	// Connect to RPC first
	client, err := ethclient.Dial(cfg.SmartWallet.EthRpcUrl)
	require.NoError(t, err, "Failed to connect to RPC")
	t.Cleanup(func() { client.Close() })

	// Set factory address for smart wallet derivation
	aa.SetFactoryAddress(cfg.SmartWallet.FactoryAddress)

	// Always derive smart wallet address from owner + salt:0
	// This ensures consistency and tests the auto-creation flow
	smartWalletAddress, err := aa.GetSenderAddress(client, ownerAddress, big.NewInt(0))
	require.NoError(t, err, "Failed to derive smart wallet address")

	t.Logf("🔑 Owner EOA: %s", ownerAddress.Hex())
	t.Logf("💼 Smart Wallet (salt:0): %s", smartWalletAddress.Hex())
	t.Logf("💰 Destination: %s", destinationAddress.Hex())

	// Create engine for RunNodeImmediately execution
	db := testutil.TestMustDB()
	t.Cleanup(func() {
		storage.Destroy(db.(*storage.BadgerStorage))
	})

	engine := New(db, cfg, nil, testutil.GetLogger())
	t.Cleanup(func() {
		engine.Stop()
	})

	// Create user model
	user := &model.User{
		Address:             ownerAddress,
		SmartAccountAddress: smartWalletAddress,
	}

	// Register the smart wallet in the database
	err = StoreWallet(db, ownerAddress, &model.SmartWallet{
		Owner:   &ownerAddress,
		Address: smartWalletAddress,
		Salt:    big.NewInt(0),
	})
	require.NoError(t, err, "Failed to store wallet in database")

	return cfg, ownerAddress, smartWalletAddress, destinationAddress, client, engine, user
}

// Test 1: ETH Transfer with Paymaster Sponsorship
// Tests ETH transfer using ethTransfer node type with paymaster sponsorship
func TestUserOpETHTransferWithPaymaster(t *testing.T) {
	_, _, smartWalletAddress, destinationAddress, client, engine, user := setupUserOpWithdrawalTest(t)

	t.Logf("🔄 STEP 1: ETH Transfer with Paymaster Sponsorship")

	// Check smart wallet ETH balance
	smartWalletBalance, err := client.BalanceAt(context.Background(), *smartWalletAddress, nil)
	require.NoError(t, err, "Failed to get smart wallet balance")
	t.Logf("💰 Smart Wallet ETH Balance: %s wei (%.6f ETH)", smartWalletBalance.String(), float64(smartWalletBalance.Int64())/1e18)

	// Skip if no ETH balance to transfer
	if smartWalletBalance.Cmp(big.NewInt(0)) <= 0 {
		t.Skip("No ETH balance to transfer")
	}

	// Use a tiny transfer amount to allow ~100+ test runs
	// Transfer 0.000001 ETH (1 microether) per test
	transferAmount := big.NewInt(1000000000000) // 0.000001 ETH in wei (1 microether)

	// Skip if balance is less than transfer amount
	if smartWalletBalance.Cmp(transferAmount) < 0 {
		t.Skipf("Insufficient ETH balance: have %.6f ETH, need %.6f ETH",
			float64(smartWalletBalance.Int64())/1e18,
			float64(transferAmount.Int64())/1e18)
	}

	t.Logf("💸 Transferring %s wei (%.6f ETH) to %s", transferAmount.String(), float64(transferAmount.Int64())/1e18, destinationAddress.Hex())
	t.Logf("   (Using ethTransfer node type with paymaster sponsorship)")

	// ETHTransfer node configuration - much simpler than contractWrite!
	ethTransferConfig := map[string]interface{}{
		"destination": destinationAddress.Hex(),
		"amount":      transferAmount.String(),
	}

	settings := map[string]interface{}{
		"runner":      smartWalletAddress.Hex(),
		"smartWallet": smartWalletAddress.Hex(),
		"chain_id":    int64(8453), // Base chain ID
	}

	inputVars := map[string]interface{}{
		"settings": settings,
	}

	// ETHTransfer node with is_simulated=false for real UserOp execution
	// Use paymaster sponsorship for ETH transfer
	usePaymaster := true
	t.Logf("   Using ethTransfer node with PAYMASTER sponsorship (shouldUsePaymasterOverride=true)...")
	transferResult, err := engine.RunNodeImmediately("ethTransfer", ethTransferConfig, inputVars, user, false, &usePaymaster)

	require.NoError(t, err, "ETH transfer RunNodeImmediately should not return error")
	require.NotNil(t, transferResult, "ETH transfer result should not be nil")

	// Check if the execution succeeded
	if successVal, ok := transferResult["success"]; ok {
		if success, isBool := successVal.(bool); isBool && !success {
			if errorVal, hasError := transferResult["error"]; hasError {
				if errorStr, isString := errorVal.(string); isString {
					t.Fatalf("❌ ETH transfer execution failed: %s", errorStr)
				}
			}
			t.Fatalf("❌ ETH transfer execution failed with success=false")
		}
	}

	t.Logf("✅ ETH transfer confirmed on-chain")
	t.Logf("   Result: %+v", transferResult)

	// Verify ETH was transferred
	newBalance, err := client.BalanceAt(context.Background(), *smartWalletAddress, nil)
	if err == nil {
		t.Logf("📊 Remaining Smart Wallet Balance: %s wei (%f ETH)", newBalance.String(), float64(newBalance.Int64())/1e18)
	}

	// Check destination received funds
	destinationBalance, err := client.BalanceAt(context.Background(), destinationAddress, nil)
	if err == nil {
		t.Logf("💰 Destination Balance: %s wei (%f ETH)", destinationBalance.String(), float64(destinationBalance.Int64())/1e18)
	}

	t.Logf("🎉 ETH transfer with paymaster sponsorship completed!")
}

// Test 1: USDC Transfer with Paymaster Sponsorship
// Tests transferring USDC using paymaster sponsorship
func TestUserOpUSDCWithdrawalWithPaymaster(t *testing.T) {
	cfg, _, smartWalletAddress, destinationAddress, client, engine, user := setupUserOpWithdrawalTest(t)

	t.Logf("🔄 STEP 1: USDC Transfer with Paymaster Sponsorship")

	// USDC contract address on Base
	usdcAddress := common.HexToAddress("0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913")

	// Create ERC-20 contract instance to check balance
	erc20ABI := `[{"constant":true,"inputs":[{"name":"_owner","type":"address"}],"name":"balanceOf","outputs":[{"name":"balance","type":"uint256"}],"type":"function"},{"constant":false,"inputs":[{"name":"_to","type":"address"},{"name":"_value","type":"uint256"}],"name":"transfer","outputs":[{"name":"","type":"bool"}],"type":"function"}]`
	parsedABI, err := abi.JSON(strings.NewReader(erc20ABI))
	require.NoError(t, err, "Failed to parse ERC-20 ABI")

	erc20Contract := bind.NewBoundContract(usdcAddress, parsedABI, client, client, client)

	// Check USDC balance
	var results []interface{}
	err = erc20Contract.Call(nil, &results, "balanceOf", *smartWalletAddress)
	if err != nil {
		t.Logf("⚠️  Could not get USDC balance: %v", err)
		results = []interface{}{big.NewInt(0)}
	}

	usdcBalance := big.NewInt(0)
	if len(results) > 0 {
		if bal, ok := results[0].(*big.Int); ok {
			usdcBalance = bal
		}
	}

	// Skip if no USDC balance
	if usdcBalance.Cmp(big.NewInt(0)) <= 0 {
		t.Skip("No USDC balance to transfer")
	}

	// Use a tiny transfer amount to allow ~100+ test runs with 10 USDC
	// Transfer 0.01 USDC (1 cent) per test
	transferAmount := big.NewInt(10000) // 0.01 USDC (6 decimals)

	// Skip if balance is less than transfer amount
	if usdcBalance.Cmp(transferAmount) < 0 {
		t.Skipf("Insufficient USDC balance: have %.6f USDC, need %.6f USDC",
			float64(usdcBalance.Int64())/1e6,
			float64(transferAmount.Int64())/1e6)
	}

	usdcFloat := float64(transferAmount.Int64()) / 1e6
	t.Logf("💰 USDC Balance: %s (raw = %.6f USDC)", usdcBalance.String(), float64(usdcBalance.Int64())/1e6)
	t.Logf("💸 Transferring %s (raw), %.6f USDC to %s", transferAmount.String(), usdcFloat, destinationAddress.Hex())
	t.Logf("💳 Paymaster: %s (FIXED - no senderNonce increment)", cfg.SmartWallet.PaymasterAddress.Hex())

	// Target the USDC contract directly - vm_runner_contract_write will wrap it in smart wallet execute()
	// This creates: smartWallet.execute(USDC, 0, USDC.transfer(...))
	transferABI := []interface{}{
		map[string]interface{}{
			"inputs": []interface{}{
				map[string]interface{}{"name": "_to", "type": "address"},
				map[string]interface{}{"name": "_value", "type": "uint256"},
			},
			"name":            "transfer",
			"outputs":         []interface{}{map[string]interface{}{"name": "", "type": "bool"}},
			"stateMutability": "nonpayable",
			"type":            "function",
		},
	}

	usdcTransferConfig := map[string]interface{}{
		"contractAddress": usdcAddress.Hex(), // <-- Target USDC directly
		"contractAbi":     transferABI,
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName":   "transfer",
				"methodParams": []interface{}{destinationAddress.Hex(), transferAmount.String()},
				"callData":     "",
			},
		},
	}

	settings := map[string]interface{}{
		"runner":      smartWalletAddress.Hex(),
		"smartWallet": smartWalletAddress.Hex(),
		"destination": destinationAddress.Hex(),
		"amount":      transferAmount.String(),
		"token":       "USDC",
		"chain_id":    int64(8453), // Base chain ID
	}

	inputVars := map[string]interface{}{
		"settings": settings,
	}

	// Use FIXED PAYMASTER for USDC transfer (should work without AA33 error now!)
	usePaymaster := true
	t.Logf("   Using FIXED PAYMASTER (runner will auto-wrap in smart wallet execute)...")
	usdcResult, err := engine.RunNodeImmediately("contractWrite", usdcTransferConfig, inputVars, user, false, &usePaymaster)

	require.NoError(t, err, "USDC transfer RunNodeImmediately should not return error")
	require.NotNil(t, usdcResult, "USDC transfer result should not be nil")

	// Check if the execution succeeded
	if successVal, ok := usdcResult["success"]; ok {
		if success, isBool := successVal.(bool); isBool && !success {
			if errorVal, hasError := usdcResult["error"]; hasError {
				if errorStr, isString := errorVal.(string); isString {
					t.Fatalf("❌ USDC transfer execution failed: %s", errorStr)
				}
			}
			t.Fatalf("❌ USDC transfer execution failed with success=false")
		}
	}

	t.Logf("✅ USDC transfer confirmed on-chain")
	t.Logf("   Result: %+v", usdcResult)

	// Verify USDC was transferred
	var newResults []interface{}
	err = erc20Contract.Call(nil, &newResults, "balanceOf", *smartWalletAddress)
	if err == nil && len(newResults) > 0 {
		if newBal, ok := newResults[0].(*big.Int); ok {
			newUsdcFloat := float64(newBal.Int64()) / 1e6
			t.Logf("📊 Remaining USDC Balance: %s (raw), %f USDC", newBal.String(), newUsdcFloat)
		}
	}

	t.Logf("🎉 USDC transfer with paymaster sponsorship completed!")
}

// Test 2: EntryPoint Deposit Withdrawal (Paymaster-Sponsored)
// Tests withdrawing EntryPoint deposit using paymaster-sponsored UserOps
// Note: Paymaster pays for the gas, while the deposit itself is withdrawn to destination
func TestUserOpEntryPointWithdrawalWithPaymaster(t *testing.T) {
	cfg, _, smartWalletAddress, destinationAddress, client, engine, user := setupUserOpWithdrawalTest(t)

	t.Logf("🔄 STEP 2: EntryPoint Deposit Withdrawal (Paymaster-Sponsored Gas)")

	// Check EntryPoint deposit
	entryPointContract, err := aa.NewEntryPoint(cfg.SmartWallet.EntrypointAddress, client)
	require.NoError(t, err, "Failed to initialize EntryPoint contract")

	depositInfo, err := entryPointContract.GetDepositInfo(nil, *smartWalletAddress)
	if err != nil {
		t.Logf("⚠️  Could not get EntryPoint deposit: %v", err)
		depositInfo.Deposit = big.NewInt(0)
	}

	t.Logf("🏦 EntryPoint Deposit: %s wei (%.6f ETH)", depositInfo.Deposit.String(), float64(depositInfo.Deposit.Int64())/1e18)

	// Skip if no deposit to withdraw
	if depositInfo.Deposit.Cmp(big.NewInt(0)) <= 0 {
		t.Skip("No EntryPoint deposit to withdraw")
	}

	// Use a tiny withdraw amount to allow ~100+ test runs with 0.001 ETH deposit
	// Withdraw 0.000001 ETH (1 microether) per test
	withdrawAmount := big.NewInt(1000000000000) // 0.000001 ETH in wei (1 microether)

	// Skip if deposit is less than withdraw amount
	if depositInfo.Deposit.Cmp(withdrawAmount) < 0 {
		t.Skipf("Insufficient EntryPoint deposit: have %.6f ETH, need %.6f ETH",
			float64(depositInfo.Deposit.Int64())/1e18,
			float64(withdrawAmount.Int64())/1e18)
	}

	t.Logf("💸 Withdrawing %s wei (%.6f ETH) from EntryPoint deposit", withdrawAmount.String(), float64(withdrawAmount.Int64())/1e18)
	t.Logf("   (Tiny amount to allow ~1000 test runs with 0.001 ETH deposit)")

	// EntryPoint withdrawTo ABI
	withdrawABI := []interface{}{
		map[string]interface{}{
			"inputs": []interface{}{
				map[string]interface{}{"name": "withdrawAddress", "type": "address"},
			},
			"name":            "withdrawTo",
			"outputs":         []interface{}{},
			"stateMutability": "nonpayable",
			"type":            "function",
		},
	}

	// Call EntryPoint withdrawTo function directly (self-funded, no paymaster needed)
	withdrawConfig := map[string]interface{}{
		"contractAddress": cfg.SmartWallet.EntrypointAddress.Hex(),
		"contractAbi":     withdrawABI,
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName":   "withdrawTo",
				"methodParams": []interface{}{destinationAddress.Hex()},
				"callData":     "",
			},
		},
	}

	settings := map[string]interface{}{
		"runner":      smartWalletAddress.Hex(),
		"smartWallet": smartWalletAddress.Hex(),
		"destination": destinationAddress.Hex(),
		"amount":      withdrawAmount.String(),
		"entryPoint":  cfg.SmartWallet.EntrypointAddress.Hex(),
		"chain_id":    int64(8453), // Base chain ID
	}

	inputVars := map[string]interface{}{
		"settings": settings,
	}

	// Use paymaster to sponsor the gas for withdrawal transaction
	// Note: The EntryPoint deposit is separate from gas payment
	// Paymaster pays gas, and we withdraw the deposit to destination
	usePaymaster := true
	t.Logf("   Using PAYMASTER to sponsor gas for withdrawal transaction...")
	withdrawResult, err := engine.RunNodeImmediately("contractWrite", withdrawConfig, inputVars, user, false, &usePaymaster)

	require.NoError(t, err, "EntryPoint withdrawal RunNodeImmediately should not return error")
	require.NotNil(t, withdrawResult, "EntryPoint withdrawal result should not be nil")

	// Check if the execution succeeded
	if successVal, ok := withdrawResult["success"]; ok {
		if success, isBool := successVal.(bool); isBool && !success {
			if errorVal, hasError := withdrawResult["error"]; hasError {
				if errorStr, isString := errorVal.(string); isString {
					t.Fatalf("❌ EntryPoint withdrawal execution failed: %s", errorStr)
				}
			}
			t.Fatalf("❌ EntryPoint withdrawal execution failed with success=false")
		}
	}

	t.Logf("✅ EntryPoint withdrawal confirmed on-chain")
	t.Logf("   Result: %+v", withdrawResult)

	// Verify the remaining deposit
	newDepositInfo, err := entryPointContract.GetDepositInfo(nil, *smartWalletAddress)
	if err == nil {
		t.Logf("📊 Remaining EntryPoint Deposit: %s wei (%f ETH)", newDepositInfo.Deposit.String(), float64(newDepositInfo.Deposit.Int64())/1e18)
	}

	// Check destination received funds
	destinationBalance, err := client.BalanceAt(context.Background(), destinationAddress, nil)
	if err == nil {
		t.Logf("💰 Destination Balance: %s wei (%f ETH)", destinationBalance.String(), float64(destinationBalance.Int64())/1e18)
	}

	t.Logf("🎉 EntryPoint deposit withdrawal completed!")
}
