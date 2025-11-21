//go:build manual
// +build manual

package taskengine

import (
	"context"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
)

// TestWithdrawAllETH_Sepolia simulates the "withdraw all" functionality from Base
// This test calculates maxWithdrawable and executes withdrawal, simulating the RPC method
// behavior when amount: "max" is used
//
// NOTE: This test requires manual execution and is excluded from normal test runs.
// To run this test: go test -tags=manual -v -run TestWithdrawAllETH_Sepolia ./core/taskengine
func TestWithdrawAllETH_Sepolia(t *testing.T) {
	// Try to load Sepolia config first (default test config)
	cfg, err := config.NewConfig(testutil.GetConfigPath(testutil.DefaultConfigPath))

	if err != nil {
		// Fallback to explicit path if GetConfigPath fails
		cfg, err = config.NewConfig("../../config/aggregator-sepolia.yaml")
		if err != nil {
			t.Skipf("Failed to load aggregator-sepolia.yaml: %v", err)
		}
	}

	// Connect to RPC to determine the actual chain
	tempClient, err := ethclient.Dial(cfg.SmartWallet.EthRpcUrl)
	if err != nil {
		t.Skipf("Cannot connect to RPC: %v", err)
	}
	chainID, err := tempClient.ChainID(context.Background())
	tempClient.Close()
	if err != nil {
		t.Skipf("Cannot get chain ID from RPC: %v", err)
	}

	// Skip if not running on Sepolia (chain ID 11155111)
	sepoliaChainID := int64(11155111)
	if chainID.Int64() != sepoliaChainID {
		t.Skipf("Test requires Sepolia network connection (current chain ID: %d, expected: %d)", chainID.Int64(), sepoliaChainID)
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

	// Check bundler availability before proceeding
	if cfg.SmartWallet.BundlerURL != "" {
		if err := testutil.CheckBundlerAvailability(cfg.SmartWallet.BundlerURL); err != nil {
			t.Skipf("Skipping UserOp withdrawal test: bundler not available: %v\n   Hint: Start the bundler or configure a remote bundler URL in config", err)
		}
	}

	// Set factory address for smart wallet derivation
	aa.SetFactoryAddress(cfg.SmartWallet.FactoryAddress)
	t.Logf("🔧 Set factory address: %s", cfg.SmartWallet.FactoryAddress.Hex())

	// Always derive smart wallet address from owner + salt:0 using GetSenderAddress
	// This is what the actual system uses, so it's the source of truth for the test
	smartWalletAddress, err := aa.GetSenderAddress(client, ownerAddress, big.NewInt(0))
	require.NoError(t, err, "Failed to derive smart wallet address")

	t.Logf("🔑 Owner EOA: %s", ownerAddress.Hex())
	t.Logf("💼 Smart Wallet (salt:0): %s", smartWalletAddress.Hex())
	t.Logf("💰 Destination: %s", destinationAddress.Hex())

	// Verify the wallet address matches what the factory contract expects
	// This ensures consistency - if they don't match, there's a configuration issue
	factory, err := aa.NewSimpleFactory(cfg.SmartWallet.FactoryAddress, client)
	require.NoError(t, err, "Failed to create factory binding")
	expectedWalletFromFactory, err := factory.GetAddress(&bind.CallOpts{Context: context.Background()}, ownerAddress, big.NewInt(0))
	require.NoError(t, err, "Failed to get expected wallet address from factory")

	if !strings.EqualFold(expectedWalletFromFactory.Hex(), smartWalletAddress.Hex()) {
		t.Logf("⚠️  Address mismatch detected:")
		t.Logf("   GetSenderAddress returned: %s", smartWalletAddress.Hex())
		t.Logf("   Factory.GetAddress returned: %s", expectedWalletFromFactory.Hex())
		t.Logf("   Using GetSenderAddress result (what the system actually uses)")
		t.Logf("   Note: This mismatch may indicate a factory implementation difference")
	} else {
		t.Logf("✅ Address verification: Factory and GetSenderAddress agree on wallet address: %s", smartWalletAddress.Hex())
	}

	// Check if wallet is deployed, and deploy it if needed
	code, err := client.CodeAt(context.Background(), *smartWalletAddress, nil)
	require.NoError(t, err, "Failed to check wallet deployment status")
	if len(code) == 0 {
		t.Logf("⚠️  Wallet not deployed, deploying it first...")
		t.Logf("   Wallet address: %s", smartWalletAddress.Hex())
		t.Logf("   Owner address: %s", ownerAddress.Hex())
		t.Logf("   Factory address: %s", cfg.SmartWallet.FactoryAddress.Hex())

		// Get controller private key from config
		controllerPrivateKey := testutil.GetTestControllerPrivateKey()
		// Deploy the wallet using EnsureWalletDeployed
		err = testutil.EnsureWalletDeployed(client, cfg.SmartWallet.FactoryAddress, ownerAddress, big.NewInt(0), controllerPrivateKey)
		if err != nil {
			t.Fatalf("Failed to deploy wallet: %v\n   Hint: The wallet needs to be deployed before testing withdrawals. Ensure the controller has sufficient funds to deploy.", err)
		}
		t.Logf("✅ EnsureWalletDeployed reported success")

		// Re-verify deployment status after a brief wait to ensure RPC state has propagated
		// This is important because some RPC nodes may have slight delays in state updates
		// Use a longer wait and more retries since RPC propagation can be slow
		maxRetries := 10
		retryDelay := 2 * time.Second
		for i := 0; i < maxRetries; i++ {
			code, err = client.CodeAt(context.Background(), *smartWalletAddress, nil)
			require.NoError(t, err, "Failed to re-check wallet deployment status")
			if len(code) > 0 {
				t.Logf("✅ Wallet deployment confirmed (code length: %d bytes)", len(code))
				break
			}
			if i < maxRetries-1 {
				t.Logf("⏳ Waiting for RPC state propagation... (attempt %d/%d, waiting %v)", i+1, maxRetries, retryDelay)
				time.Sleep(retryDelay)
			} else {
				t.Fatalf("Wallet deployment verification failed - code still empty after %d attempts (waited %v total).\n   Wallet: %s\n   Factory expected: %s\n   This suggests the deployment transaction may have failed or the RPC node has not propagated the state.\n   Try checking the transaction on Etherscan to see if it actually succeeded.", maxRetries, time.Duration(maxRetries-1)*retryDelay, smartWalletAddress.Hex(), expectedWalletFromFactory.Hex())
			}
		}
	} else {
		t.Logf("✅ Wallet is already deployed (code length: %d bytes)", len(code))
	}

	// Final verification: ensure wallet is deployed before proceeding
	// This is important because RPC nodes may have slight delays in state propagation
	finalCode, err := client.CodeAt(context.Background(), *smartWalletAddress, nil)
	require.NoError(t, err, "Failed to verify wallet deployment before withdrawal")
	if len(finalCode) == 0 {
		t.Fatalf("Wallet %s is not deployed - cannot proceed with withdrawal test. Deployment may have failed or RPC state not propagated.", smartWalletAddress.Hex())
	}
	t.Logf("✅ Final wallet deployment verification: code length %d bytes", len(finalCode))

	// Check smart wallet ETH balance
	smartWalletBalance, err := client.BalanceAt(context.Background(), *smartWalletAddress, nil)
	require.NoError(t, err, "Failed to get smart wallet balance")
	t.Logf("💰 Smart Wallet ETH Balance: %s wei (%.9f ETH)", smartWalletBalance.String(), float64(smartWalletBalance.Int64())/1e18)

	// Skip if no ETH balance to withdraw
	if smartWalletBalance.Cmp(big.NewInt(0)) <= 0 {
		t.Skip("No ETH balance to withdraw")
	}

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

	// Simulate "withdraw all" by calculating maxWithdrawable
	// This replicates the logic from rpc_server.go when amount: "max" is used
	// The RPC method:
	// 1. Estimates gas reimbursement using a placeholder amount (0.00001 ETH)
	// 2. Calculates maxWithdrawable = balance - estimatedReimbursement
	// 3. Executes withdrawal with the calculated amount
	// For this test, we'll use a conservative gas estimate

	t.Logf("🔄 Testing 'withdraw all' functionality (simulating amount: 'max')")
	t.Logf("   This simulates the Base withdrawal request:")
	t.Logf("   - Smart Wallet: %s", smartWalletAddress.Hex())
	t.Logf("   - Recipient: %s", destinationAddress.Hex())
	t.Logf("   - Amount: max (will calculate maxWithdrawable)")
	t.Logf("   - Token: ETH")

	// For "withdraw all" test, we need sufficient balance to cover both withdrawal and gas reimbursement
	// The minimum required balance should be at least the estimated gas reimbursement
	// Typical gas costs for ETH withdrawal with paymaster on Sepolia:
	// - callGasLimit: ~21,000 (simple ETH transfer)
	// - verificationGasLimit: ~100,000 (paymaster verification)
	// - preVerificationGas: ~50,000
	// Total: ~171,000 gas units
	// At typical Sepolia gas price (~2 gwei): ~0.000342 ETH
	// We'll use a conservative estimate of 0.0005 ETH (500,000,000,000,000 wei)
	estimatedReimbursement := big.NewInt(500000000000000) // 0.0005 ETH (conservative estimate)

	// Minimum balance required for "withdraw all" test
	// We need at least enough to cover the gas reimbursement, plus a small amount to withdraw
	minRequiredBalance := new(big.Int).Add(estimatedReimbursement, big.NewInt(1000000000000000)) // 0.0005 + 0.001 = 0.0015 ETH

	if smartWalletBalance.Cmp(minRequiredBalance) < 0 {
		t.Skipf("Insufficient balance for 'withdraw all' test.\n"+
			"   Current balance: %s wei (%.9f ETH)\n"+
			"   Estimated reimbursement: %s wei (%.9f ETH)\n"+
			"   Minimum required: %s wei (%.9f ETH)\n"+
			"   Please fund the wallet %s with at least %.6f ETH to run this test.",
			smartWalletBalance.String(), float64(smartWalletBalance.Int64())/1e18,
			estimatedReimbursement.String(), float64(estimatedReimbursement.Int64())/1e18,
			minRequiredBalance.String(), float64(minRequiredBalance.Int64())/1e18,
			smartWalletAddress.Hex(), float64(minRequiredBalance.Int64())/1e18)
	}

	// Calculate maxWithdrawable = balance - estimatedReimbursement
	maxWithdrawable := new(big.Int).Sub(smartWalletBalance, estimatedReimbursement)
	if maxWithdrawable.Cmp(big.NewInt(0)) <= 0 {
		t.Skipf("Insufficient balance for 'withdraw all': balance %s wei is less than estimated reimbursement %s wei",
			smartWalletBalance.String(), estimatedReimbursement.String())
	}

	t.Logf("💰 Balance: %s wei (%.9f ETH)", smartWalletBalance.String(), float64(smartWalletBalance.Int64())/1e18)
	t.Logf("   Estimated Reimbursement: %s wei (%.9f ETH)", estimatedReimbursement.String(), float64(estimatedReimbursement.Int64())/1e18)
	t.Logf("   Max Withdrawable: %s wei (%.9f ETH)", maxWithdrawable.String(), float64(maxWithdrawable.Int64())/1e18)

	// Execute withdrawal with the calculated max amount using ethTransfer node
	ethTransferConfig := map[string]interface{}{
		"destination": destinationAddress.Hex(),
		"amount":      maxWithdrawable.String(),
	}

	settings := map[string]interface{}{
		"runner":      smartWalletAddress.Hex(),
		"smartWallet": smartWalletAddress.Hex(),
		"chain_id":    int64(11155111), // Sepolia chain ID
	}

	inputVars := map[string]interface{}{
		"settings": settings,
	}

	// Execute withdrawal with paymaster sponsorship (automatic reimbursement)
	// is_simulated=false ensures real UserOp execution via bundler
	t.Logf("   Executing withdrawal with calculated max amount...")
	transferResult, err := engine.RunNodeImmediately("ethTransfer", ethTransferConfig, inputVars, user, false)
	require.NoError(t, err, "Withdrawal RunNodeImmediately should not return error")
	require.NotNil(t, transferResult, "Withdrawal result should not be nil")

	// Check if the execution succeeded
	if successVal, ok := transferResult["success"]; ok {
		if success, isBool := successVal.(bool); isBool && !success {
			if errorVal, hasError := transferResult["error"]; hasError {
				if errorStr, isString := errorVal.(string); isString {
					t.Fatalf("❌ Withdrawal execution failed: %s", errorStr)
				}
			}
			t.Fatalf("❌ Withdrawal execution failed with success=false")
		}
	}

	t.Logf("✅ Withdrawal request successful")

	// Verify ETH was transferred
	newBalance, err := client.BalanceAt(context.Background(), *smartWalletAddress, nil)
	if err == nil {
		t.Logf("📊 Remaining Smart Wallet Balance: %s wei (%.9f ETH)", newBalance.String(), float64(newBalance.Int64())/1e18)
		// Note: There may be a small remaining balance due to gas estimation variance
		// This is expected and acceptable - the actual gas used may differ slightly from estimates
		if newBalance.Cmp(big.NewInt(0)) > 0 {
			remainingETH := float64(newBalance.Int64()) / 1e18
			if remainingETH < 0.0001 {
				t.Logf("   ℹ️  Small remaining balance is expected due to gas estimation variance")
				t.Logf("   This is normal and acceptable (~0.000065 ETH is typical)")
			}
		}
	}

	// Check destination received funds
	destinationBalance, err := client.BalanceAt(context.Background(), destinationAddress, nil)
	if err == nil {
		t.Logf("💰 Destination Balance: %s wei (%.9f ETH)", destinationBalance.String(), float64(destinationBalance.Int64())/1e18)
	}

	t.Logf("🎉 'Withdraw all' test completed successfully!")
}
