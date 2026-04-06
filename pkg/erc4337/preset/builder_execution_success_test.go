package preset

import (
	"context"
	"math/big"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"
)

// TestUserOpWithdrawalSkipsReimbursementWhenBalanceInsufficient tests that when
// the wallet balance can't cover both withdrawal + reimbursement, the system
// gracefully skips reimbursement and still completes the withdrawal.
//
// To avoid draining the primary test wallet (salt=0) across CI runs, this test:
// 1. Withdraws from the primary wallet to a secondary wallet (salt=1)
// 2. Sends the funds back from the secondary wallet to the primary wallet
func TestUserOpWithdrawalSkipsReimbursementWhenBalanceInsufficient(t *testing.T) {
	smartWalletConfig := mockGetBaseTestSmartWalletConfig()

	client, err := ethclient.Dial(smartWalletConfig.EthRpcUrl)
	require.NoError(t, err, "Failed to connect to RPC")
	defer client.Close()

	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

	ownerAddr, ok := testutil.MustGetTestOwnerAddress()
	if !ok {
		t.Fatal("OWNER_EOA or TEST_PRIVATE_KEY environment variable not set")
	}
	owner := *ownerAddr

	// Primary wallet (salt=0) — the main test wallet
	primarySalt := big.NewInt(0)
	primaryWallet := testutil.GetTestSmartWalletAddress(t, client, smartWalletConfig.FactoryAddress, owner, primarySalt)

	// Secondary wallet (salt=1) — temporary recipient for withdrawn funds
	secondarySalt := big.NewInt(1)
	secondaryWallet := testutil.GetTestSmartWalletAddress(t, client, smartWalletConfig.FactoryAddress, owner, secondarySalt)

	// Recover any leftover funds in the secondary wallet from a previous run.
	// This handles the case where a prior CI run drained primary → secondary but
	// the return transfer didn't complete (e.g., timeout or CI cancellation).
	secondaryPreBalance, err := client.BalanceAt(context.Background(), secondaryWallet, nil)
	require.NoError(t, err, "Failed to get secondary wallet pre-balance")
	if secondaryPreBalance.Cmp(big.NewInt(0)) > 0 {
		t.Logf("Recovering %s wei from secondary wallet (leftover from previous run)", secondaryPreBalance.String())
		recoverCalldata, err := aa.PackExecute(primaryWallet, secondaryPreBalance, []byte{})
		require.NoError(t, err, "Failed to pack recover calldata")

		recoverPaymaster := GetVerifyingPaymasterRequestForDuration(
			smartWalletConfig.PaymasterAddress,
			15*time.Minute,
		)
		_, recoverReceipt, err := SendUserOp(
			smartWalletConfig,
			owner,
			recoverCalldata,
			recoverPaymaster,
			&secondaryWallet,
			secondarySalt,
			nil,
			nil, // executionFeeWei
		)
		require.NoError(t, err, "Recovery transfer should succeed")
		if recoverReceipt != nil {
			t.Logf("Recovery succeeded. TX Hash: %s Gas used: %d", recoverReceipt.TxHash.Hex(), recoverReceipt.GasUsed)
		}
	}

	balance, err := client.BalanceAt(context.Background(), primaryWallet, nil)
	require.NoError(t, err, "Failed to get wallet balance")

	t.Logf("Primary Wallet: %s, Balance: %s wei (%.6f ETH)", primaryWallet.Hex(), balance.String(), float64(balance.Int64())/1e18)
	t.Logf("Secondary Wallet: %s (recipient for withdrawal)", secondaryWallet.Hex())

	// Withdraw most of the balance to the secondary wallet so there's not enough
	// left for reimbursement. The system should skip reimbursement wrapping.
	reserve := big.NewInt(100000000000000) // 0.0001 ETH
	withdrawalAmount := new(big.Int).Sub(balance, reserve)

	calldata, err := aa.PackExecute(secondaryWallet, withdrawalAmount, []byte{})
	require.NoError(t, err, "Failed to pack execute calldata")

	paymasterRequest := GetVerifyingPaymasterRequestForDuration(
		smartWalletConfig.PaymasterAddress,
		15*time.Minute,
	)

	// Withdrawal should succeed — system skips reimbursement when balance is insufficient
	userOp, receipt, err := SendUserOp(
		smartWalletConfig,
		owner,
		calldata,
		paymasterRequest,
		&primaryWallet,
		nil,
		nil, // executionFeeWei
		nil,
	)
	require.NoError(t, err, "Withdrawal should succeed even without reimbursement")
	require.NotNil(t, userOp, "UserOp should be built")
	if receipt == nil {
		t.Skip("UserOp sent but receipt not available (confirmation timeout)")
	}
	t.Logf("Withdrawal succeeded. TX Hash: %s Gas used: %d", receipt.TxHash.Hex(), receipt.GasUsed)

	// Send the funds back from the secondary wallet to the primary wallet
	secondaryBalance, err := client.BalanceAt(context.Background(), secondaryWallet, nil)
	require.NoError(t, err, "Failed to get secondary wallet balance")
	t.Logf("Secondary Wallet Balance after withdrawal: %s wei (%.6f ETH)", secondaryBalance.String(), float64(secondaryBalance.Int64())/1e18)

	if secondaryBalance.Cmp(big.NewInt(0)) > 0 {
		returnCalldata, err := aa.PackExecute(primaryWallet, secondaryBalance, []byte{})
		require.NoError(t, err, "Failed to pack return calldata")

		returnPaymasterRequest := GetVerifyingPaymasterRequestForDuration(
			smartWalletConfig.PaymasterAddress,
			15*time.Minute,
		)

		_, returnReceipt, err := SendUserOp(
			smartWalletConfig,
			owner,
			returnCalldata,
			returnPaymasterRequest,
			&secondaryWallet,
			secondarySalt,
			nil,
			nil, // executionFeeWei
		)
		require.NoError(t, err, "Return transfer should succeed")
		if returnReceipt != nil {
			t.Logf("Funds returned to primary wallet. TX Hash: %s Gas used: %d", returnReceipt.TxHash.Hex(), returnReceipt.GasUsed)
		}
	}
}

// TestUserOpExecutionFailureExcessiveTransfer tests execution failure when
// attempting to transfer more ETH than the wallet has (simple revert scenario)
func TestUserOpExecutionFailureExcessiveTransfer(t *testing.T) {
	smartWalletConfig := mockGetBaseTestSmartWalletConfig()

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
	_, receipt, err := SendUserOp(
		smartWalletConfig,
		owner,
		calldata,
		nil,                 // No paymaster
		&smartWalletAddress, // Use factory's address as sender override
		nil,                 // No salt override
		nil,                 // No execution fee
		nil,                 // No logger
	)

	// Execution should fail due to excessive transfer amount
	if err == nil && receipt == nil {
		t.Skip("UserOp sent but receipt not available (confirmation timeout)")
	}
	require.Error(t, err, "Expected execution failure due to excessive transfer amount")
	if receipt != nil {
		t.Logf("Transaction included in block: %s (gas used: %d)", receipt.TxHash.Hex(), receipt.GasUsed)
	}
}

// TestUserOpExecutionSuccessWithPaymaster tests that successful executions
// are properly detected (positive test case to ensure the code works correctly)
func TestUserOpExecutionSuccessWithPaymaster(t *testing.T) {
	smartWalletConfig := mockGetBaseTestSmartWalletConfig()

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
		nil,                 // No salt override
		nil,                 // No execution fee
		nil,                 // No logger
	)

	// UserOp should be built, sent, and executed successfully
	require.NotNil(t, userOp, "UserOp should be built and sent")
	require.NoError(t, err, "UserOp execution should succeed")
	if receipt == nil {
		t.Skip("UserOp sent but receipt not available (confirmation timeout)")
	}

	t.Logf("Transaction executed successfully. TX Hash: %s Gas used: %d", receipt.TxHash.Hex(), receipt.GasUsed)
}
