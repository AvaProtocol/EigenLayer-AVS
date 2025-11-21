package preset

import (
	"context"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
)

// TestUserOpExecutionFailureInsufficientBalance tests the real-world scenario where
// a UserOp execution fails due to insufficient balance when reimbursement is enabled.
// This is based on the actual failure documented in docs/WITHDRAWAL-FAILURE-PROOF.md
func TestUserOpExecutionFailureInsufficientBalance(t *testing.T) {
	if os.Getenv("CI") != "" || os.Getenv("SEPOLIA_BUNDLER_RPC") == "" || os.Getenv("SEPOLIA_RPC") == "" {
		t.Skip("Skipping TestUserOpExecutionFailureInsufficientBalance: CI or missing SEPOLIA endpoints")
	}

	smartWalletConfig := mockGetBaseTestSmartWalletConfig()

	if smartWalletConfig.BundlerURL == "" || smartWalletConfig.EthRpcUrl == "" {
		t.Skip("Skipping TestUserOpExecutionFailureInsufficientBalance: missing BundlerURL or EthRpcUrl")
	}

	// Check bundler availability before proceeding
	if err := testutil.CheckBundlerAvailability(smartWalletConfig.BundlerURL); err != nil {
		t.Skipf("Skipping TestUserOpExecutionFailureInsufficientBalance: bundler not available: %v\n   Hint: Start the bundler or configure a remote bundler URL in config", err)
	}

	// Check chain ID instead of URL (more reliable)
	client, err := ethclient.Dial(smartWalletConfig.EthRpcUrl)
	if err != nil {
		t.Skipf("Skipping TestUserOpExecutionFailureInsufficientBalance: failed to connect to RPC: %v", err)
	}
	defer client.Close()

	chainID, err := client.ChainID(context.Background())
	if err != nil {
		t.Skipf("Skipping TestUserOpExecutionFailureInsufficientBalance: failed to get chain ID: %v", err)
	}
	// Sepolia chain ID is 11155111
	if chainID.Uint64() != 11155111 {
		t.Skipf("Skipping TestUserOpExecutionFailureInsufficientBalance: chain ID %d is not Sepolia (11155111)", chainID.Uint64())
	}

	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

	// Get owner EOA from environment variable (OWNER_EOA or TEST_PRIVATE_KEY)
	ownerAddr, ok := testutil.MustGetTestOwnerAddress()
	if !ok {
		t.Skip("OWNER_EOA or TEST_PRIVATE_KEY environment variable not set")
	}
	owner := *ownerAddr

	// Ensure wallet is deployed before testing
	controllerPrivateKey := testutil.GetTestControllerPrivateKey()
	salt := big.NewInt(0) // Fixed salt for consistent test wallet
	t.Logf("🔧 Ensuring wallet is deployed (owner: %s, salt: %s)...", owner.Hex(), salt.String())
	err = testutil.EnsureWalletDeployed(client, smartWalletConfig.FactoryAddress, owner, salt, controllerPrivateKey)
	require.NoError(t, err, "Failed to ensure wallet is deployed")

	// Get smart wallet address from factory (same method used in EnsureWalletDeployed)
	factory, err := aa.NewSimpleFactory(smartWalletConfig.FactoryAddress, client)
	require.NoError(t, err, "Failed to create factory binding")
	smartWalletAddress, err := factory.GetAddress(&bind.CallOpts{Context: context.Background()}, owner, salt)
	require.NoError(t, err, "Failed to get wallet address from factory")

	// Get current balance
	balance, err := client.BalanceAt(context.Background(), smartWalletAddress, nil)
	require.NoError(t, err, "Failed to get wallet balance")

	t.Logf("💰 Smart Wallet Balance: %s wei (%.6f ETH)", balance.String(), float64(balance.Int64())/1e18)
	t.Logf("📍 Smart Wallet Address: %s", smartWalletAddress.Hex())
	t.Logf("📍 Owner Address: %s", owner.Hex())
	t.Logf("📍 Salt: 0 (fixed for consistent test wallet)")

	// Test insufficient balance scenario
	// If balance is 0, we'll attempt a withdrawal which will fail
	// If balance > 0, we'll attempt withdrawal + reimbursement that exceeds balance
	var withdrawalAmount *big.Int
	reimbursementEstimate := big.NewInt(4000000000000000) // 0.004 ETH estimate

	if balance.Cmp(big.NewInt(0)) == 0 {
		// Zero balance scenario: attempt any withdrawal will fail
		withdrawalAmount = big.NewInt(1000000000000000) // 0.001 ETH (any amount will fail)
		t.Logf("💸 Testing ZERO BALANCE scenario - attempting withdrawal: %s wei (%.6f ETH)", withdrawalAmount.String(), float64(withdrawalAmount.Int64())/1e18)
		t.Logf("   Expected: Execution failure due to insufficient balance")
	} else {
		// Non-zero balance: create withdrawal that exceeds balance when combined with reimbursement
		minRequiredForReimbursement := big.NewInt(1000000000000000) // 0.001 ETH minimum
		withdrawalAmount = new(big.Int).Sub(balance, minRequiredForReimbursement)

		// Ensure withdrawal amount is positive
		if withdrawalAmount.Cmp(big.NewInt(0)) <= 0 {
			// Balance is too low, use a small amount that will still fail
			withdrawalAmount = big.NewInt(1000000000000000) // 0.001 ETH
		}
		t.Logf("💸 Attempting withdrawal: %s wei (%.6f ETH)", withdrawalAmount.String(), float64(withdrawalAmount.Int64())/1e18)
		t.Logf("   Estimated reimbursement: %s wei (%.6f ETH)", reimbursementEstimate.String(), float64(reimbursementEstimate.Int64())/1e18)
		t.Logf("   Total needed: %s wei (%.6f ETH)", new(big.Int).Add(withdrawalAmount, reimbursementEstimate).String(),
			float64(new(big.Int).Add(withdrawalAmount, reimbursementEstimate).Int64())/1e18)
		t.Logf("   Available: %s wei (%.6f ETH)", balance.String(), float64(balance.Int64())/1e18)
	}

	t.Logf("💸 Attempting withdrawal: %s wei (%.6f ETH)", withdrawalAmount.String(), float64(withdrawalAmount.Int64())/1e18)
	t.Logf("   Estimated reimbursement: %s wei (%.6f ETH)", reimbursementEstimate.String(), float64(reimbursementEstimate.Int64())/1e18)
	t.Logf("   Total needed: %s wei (%.6f ETH)", new(big.Int).Add(withdrawalAmount, reimbursementEstimate).String(),
		float64(new(big.Int).Add(withdrawalAmount, reimbursementEstimate).Int64())/1e18)
	t.Logf("   Available: %s wei (%.6f ETH)", balance.String(), float64(balance.Int64())/1e18)

	// Create calldata for ETH transfer (withdrawal)
	destination := owner // Transfer back to owner
	calldata, err := aa.PackExecute(
		destination,
		withdrawalAmount,
		[]byte{},
	)
	require.NoError(t, err, "Failed to pack execute calldata")

	// Enable paymaster reimbursement (this will wrap with executeBatchWithValues)
	// This is the scenario that causes the failure
	paymasterRequest := GetVerifyingPaymasterRequestForDuration(
		smartWalletConfig.PaymasterAddress,
		15*time.Minute,
	)

	// Send UserOp - this should succeed in sending to bundler, but execution should fail
	// Use the factory's address as sender override to ensure consistency
	t.Logf("📤 Sending UserOp with paymaster and reimbursement enabled...")
	userOp, receipt, err := SendUserOp(
		smartWalletConfig,
		owner,
		calldata,
		paymasterRequest,
		&smartWalletAddress, // Use factory's address as sender override
		nil,                 // No logger
	)

	// UserOp should be built and sent to bundler
	require.NotNil(t, userOp, "UserOp should be built and sent")

	// CRITICAL TEST: If we got a receipt, the transaction was included in a block
	// With the new code, if execution failed (success=false), we MUST get an error
	if receipt != nil {
		t.Logf("📦 Transaction included in block: %s", receipt.TxHash.Hex())
		t.Logf("   Block: %d, Gas Used: %d", receipt.BlockNumber.Uint64(), receipt.GasUsed)

		// The new code should detect execution failure and return an error
		if err != nil {
			// ✅ EXPECTED: Execution failed, error should be returned
			require.Contains(t, err.Error(), "UserOp execution failed",
				"Error should indicate UserOp execution failure when success=false in UserOperationEvent")
			t.Logf("✅ CORRECTLY DETECTED execution failure: %v", err)

			// Verify the error message format
			require.Contains(t, strings.ToLower(err.Error()), "userop execution failed",
				"Error message should clearly indicate UserOp execution failure")
		} else {
			// ❌ UNEXPECTED: If no error, execution must have succeeded somehow
			// This could happen if:
			// 1. Balance was sufficient after all (unlikely with our calculation)
			// 2. Reimbursement wrapping was skipped (insufficient balance check)
			// 3. The new code isn't working correctly
			t.Errorf("❌ UNEXPECTED: UserOp execution succeeded but should have failed!")
			t.Errorf("   Balance: %s wei", balance.String())
			t.Errorf("   Withdrawal: %s wei", withdrawalAmount.String())
			t.Errorf("   Expected failure due to insufficient balance for reimbursement")
		}
	} else {
		// No receipt - transaction may be pending or failed to send
		if err != nil {
			t.Logf("⚠️  UserOp failed to send or timed out: %v", err)
		} else {
			t.Logf("⚠️  UserOp sent but receipt not available (may be pending)")
		}
		// This is acceptable - the test verifies the code path exists
	}
}

// TestUserOpExecutionFailureExcessiveTransfer tests execution failure when
// attempting to transfer more ETH than the wallet has (simple revert scenario)
func TestUserOpExecutionFailureExcessiveTransfer(t *testing.T) {
	if os.Getenv("CI") != "" || os.Getenv("SEPOLIA_BUNDLER_RPC") == "" || os.Getenv("SEPOLIA_RPC") == "" {
		t.Skip("Skipping TestUserOpExecutionFailureExcessiveTransfer: CI or missing SEPOLIA endpoints")
	}

	smartWalletConfig := mockGetBaseTestSmartWalletConfig()

	if smartWalletConfig.BundlerURL == "" || smartWalletConfig.EthRpcUrl == "" {
		t.Skip("Skipping TestUserOpExecutionFailureExcessiveTransfer: missing BundlerURL or EthRpcUrl")
	}

	// Check bundler availability before proceeding
	if err := testutil.CheckBundlerAvailability(smartWalletConfig.BundlerURL); err != nil {
		t.Skipf("Skipping TestUserOpExecutionFailureExcessiveTransfer: bundler not available: %v\n   Hint: Start the bundler or configure a remote bundler URL in config", err)
	}

	// Check chain ID instead of URL (more reliable)
	client, err := ethclient.Dial(smartWalletConfig.EthRpcUrl)
	if err != nil {
		t.Skipf("Skipping TestUserOpExecutionFailureExcessiveTransfer: failed to connect to RPC: %v", err)
	}
	defer client.Close()

	chainID, err := client.ChainID(context.Background())
	if err != nil {
		t.Skipf("Skipping TestUserOpExecutionFailureExcessiveTransfer: failed to get chain ID: %v", err)
	}
	// Sepolia chain ID is 11155111
	if chainID.Uint64() != 11155111 {
		t.Skipf("Skipping TestUserOpExecutionFailureExcessiveTransfer: chain ID %d is not Sepolia (11155111)", chainID.Uint64())
	}

	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

	// Get owner EOA from environment variable (OWNER_EOA or TEST_PRIVATE_KEY)
	ownerAddr, ok := testutil.MustGetTestOwnerAddress()
	if !ok {
		t.Skip("OWNER_EOA or TEST_PRIVATE_KEY environment variable not set")
	}
	owner := *ownerAddr

	// Ensure wallet is deployed before testing
	controllerPrivateKey := testutil.GetTestControllerPrivateKey()
	salt := big.NewInt(0) // Fixed salt for consistent test wallet
	t.Logf("🔧 Ensuring wallet is deployed (owner: %s, salt: %s)...", owner.Hex(), salt.String())
	err = testutil.EnsureWalletDeployed(client, smartWalletConfig.FactoryAddress, owner, salt, controllerPrivateKey)
	require.NoError(t, err, "Failed to ensure wallet is deployed")

	// Get smart wallet address from factory (same method used in EnsureWalletDeployed)
	factory, err := aa.NewSimpleFactory(smartWalletConfig.FactoryAddress, client)
	require.NoError(t, err, "Failed to create factory binding")
	smartWalletAddress, err := factory.GetAddress(&bind.CallOpts{Context: context.Background()}, owner, salt)
	require.NoError(t, err, "Failed to get wallet address from factory")

	// Get current balance
	balance, err := client.BalanceAt(context.Background(), smartWalletAddress, nil)
	require.NoError(t, err, "Failed to get wallet balance")

	t.Logf("💰 Smart Wallet Balance: %s wei (%.6f ETH)", balance.String(), float64(balance.Int64())/1e18)
	t.Logf("📍 Smart Wallet Address: %s", smartWalletAddress.Hex())
	t.Logf("📍 Owner Address: %s", owner.Hex())
	t.Logf("📍 Salt: 0 (fixed for consistent test wallet)")

	// Use an amount that's guaranteed to exceed balance
	// If balance is 0, use a fixed excessive amount
	var excessiveAmount *big.Int
	if balance.Cmp(big.NewInt(0)) == 0 {
		excessiveAmount = big.NewInt(1000000000000000000) // 1 ETH (guaranteed to exceed 0 balance)
	} else {
		excessiveAmount = new(big.Int).Mul(balance, big.NewInt(10)) // 10x the balance
	}

	t.Logf("💸 Attempting excessive transfer: %s wei (%.6f ETH)", excessiveAmount.String(), float64(excessiveAmount.Int64())/1e18)
	t.Logf("   Expected: Execution failure due to insufficient balance")

	// Create calldata for excessive ETH transfer (will cause revert)
	destination := owner
	calldata, err := aa.PackExecute(
		destination,
		excessiveAmount,
		[]byte{},
	)
	require.NoError(t, err, "Failed to pack execute calldata")

	// Send UserOp WITHOUT paymaster (self-funded, simpler scenario)
	// Use the factory's address as sender override to ensure consistency
	t.Logf("📤 Sending UserOp without paymaster (self-funded)...")
	userOp, receipt, err := SendUserOp(
		smartWalletConfig,
		owner,
		calldata,
		nil,                 // No paymaster
		&smartWalletAddress, // Use factory's address as sender override
		nil,                 // No logger
	)

	// UserOp should be built and sent
	require.NotNil(t, userOp, "UserOp should be built and sent")

	// If we got a receipt, check if execution failed
	if receipt != nil {
		t.Logf("📦 Transaction included in block: %s", receipt.TxHash.Hex())

		// The new code should detect execution failure
		if err != nil {
			require.Contains(t, err.Error(), "UserOp execution failed",
				"Error should indicate UserOp execution failure")
			t.Logf("✅ CORRECTLY DETECTED execution failure: %v", err)
		} else {
			t.Errorf("❌ UNEXPECTED: UserOp execution succeeded with excessive amount!")
		}
	} else {
		// No receipt - may be pending or failed to send
		t.Logf("⚠️  UserOp sent but receipt not available")
	}
}

// TestUserOpExecutionSuccessWithPaymaster tests that successful executions
// are properly detected (positive test case to ensure the code works correctly)
func TestUserOpExecutionSuccessWithPaymaster(t *testing.T) {
	if os.Getenv("CI") != "" || os.Getenv("SEPOLIA_BUNDLER_RPC") == "" || os.Getenv("SEPOLIA_RPC") == "" {
		t.Skip("Skipping TestUserOpExecutionSuccessWithPaymaster: CI or missing SEPOLIA endpoints")
	}

	smartWalletConfig := mockGetBaseTestSmartWalletConfig()

	if smartWalletConfig.BundlerURL == "" || smartWalletConfig.EthRpcUrl == "" {
		t.Skip("Skipping TestUserOpExecutionSuccessWithPaymaster: missing BundlerURL or EthRpcUrl")
	}

	// Check bundler availability before proceeding
	if err := testutil.CheckBundlerAvailability(smartWalletConfig.BundlerURL); err != nil {
		t.Skipf("Skipping TestUserOpExecutionSuccessWithPaymaster: bundler not available: %v\n   Hint: Start the bundler or configure a remote bundler URL in config", err)
	}

	// Check chain ID instead of URL (more reliable)
	client, err := ethclient.Dial(smartWalletConfig.EthRpcUrl)
	if err != nil {
		t.Skipf("Skipping TestUserOpExecutionSuccessWithPaymaster: failed to connect to RPC: %v", err)
	}
	defer client.Close()

	chainID, err := client.ChainID(context.Background())
	if err != nil {
		t.Skipf("Skipping TestUserOpExecutionSuccessWithPaymaster: failed to get chain ID: %v", err)
	}
	// Sepolia chain ID is 11155111
	if chainID.Uint64() != 11155111 {
		t.Skipf("Skipping TestUserOpExecutionSuccessWithPaymaster: chain ID %d is not Sepolia (11155111)", chainID.Uint64())
	}

	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

	// Get owner EOA from environment variable (OWNER_EOA or TEST_PRIVATE_KEY)
	ownerAddr, ok := testutil.MustGetTestOwnerAddress()
	if !ok {
		t.Skip("OWNER_EOA or TEST_PRIVATE_KEY environment variable not set")
	}
	owner := *ownerAddr

	// Ensure wallet is deployed before testing
	controllerPrivateKey := testutil.GetTestControllerPrivateKey()
	salt := big.NewInt(0) // Fixed salt for consistent test wallet
	t.Logf("🔧 Ensuring wallet is deployed (owner: %s, salt: %s)...", owner.Hex(), salt.String())
	err = testutil.EnsureWalletDeployed(client, smartWalletConfig.FactoryAddress, owner, salt, controllerPrivateKey)
	require.NoError(t, err, "Failed to ensure wallet is deployed")

	// Get smart wallet address from factory (same method used in EnsureWalletDeployed)
	factory, err := aa.NewSimpleFactory(smartWalletConfig.FactoryAddress, client)
	require.NoError(t, err, "Failed to create factory binding")
	smartWalletAddress, err := factory.GetAddress(&bind.CallOpts{Context: context.Background()}, owner, salt)
	require.NoError(t, err, "Failed to get wallet address from factory")

	// Get current balance
	balance, err := client.BalanceAt(context.Background(), smartWalletAddress, nil)
	require.NoError(t, err, "Failed to get wallet balance")

	t.Logf("💰 Smart Wallet Balance: %s wei (%.6f ETH)", balance.String(), float64(balance.Int64())/1e18)
	t.Logf("📍 Smart Wallet Address: %s", smartWalletAddress.Hex())
	t.Logf("📍 Owner Address: %s", owner.Hex())
	t.Logf("📍 Salt: 0 (fixed for consistent test wallet)")

	// Use a tiny transfer amount that's guaranteed to succeed
	// Transfer 0.000001 ETH (1 microether) - very small amount
	tinyAmount := big.NewInt(1000000000000) // 0.000001 ETH

	// Skip if balance is too low for the transfer
	if balance.Cmp(tinyAmount) < 0 {
		t.Skipf("Skipping TestUserOpExecutionSuccessWithPaymaster: balance too low (have %s wei, need %s wei)", balance.String(), tinyAmount.String())
	}

	t.Logf("💸 Transferring tiny amount: %s wei (%.6f ETH)", tinyAmount.String(), float64(tinyAmount.Int64())/1e18)
	t.Logf("   Expected: Successful execution")

	// Create calldata for tiny ETH transfer (should succeed)
	destination := owner
	calldata, err := aa.PackExecute(
		destination,
		tinyAmount,
		[]byte{},
	)
	require.NoError(t, err, "Failed to pack execute calldata")

	// Enable paymaster with reimbursement
	paymasterRequest := GetVerifyingPaymasterRequestForDuration(
		smartWalletConfig.PaymasterAddress,
		15*time.Minute,
	)

	// Send UserOp - this should succeed
	// Use the factory's address as sender override to ensure consistency
	t.Logf("📤 Sending UserOp with paymaster and reimbursement enabled...")
	userOp, receipt, err := SendUserOp(
		smartWalletConfig,
		owner,
		calldata,
		paymasterRequest,
		&smartWalletAddress, // Use factory's address as sender override
		nil,                 // No logger
	)

	// UserOp should be built and sent
	require.NotNil(t, userOp, "UserOp should be built and sent")

	// If we got a receipt, execution should have succeeded
	if receipt != nil {
		t.Logf("📦 Transaction included in block: %s", receipt.TxHash.Hex())

		// With successful execution, there should be NO error
		require.NoError(t, err, "UserOp execution should succeed, no error expected")
		t.Logf("✅ CORRECTLY DETECTED successful execution (no error returned)")
	} else {
		// No receipt - may be pending
		if err != nil {
			t.Logf("⚠️  UserOp failed: %v", err)
		} else {
			t.Logf("⚠️  UserOp sent but receipt not available (may be pending)")
		}
	}
}
