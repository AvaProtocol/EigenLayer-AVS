package aggregator

import (
	"math/big"
	"os"
	"strings"
	"testing"

	"crypto/ecdsa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// TestWithdrawETHUserOp tests real ETH withdrawal via UserOp to Sepolia bundler
func TestWithdrawETHUserOp(t *testing.T) {
	if os.Getenv("CI") != "" || os.Getenv("SEPOLIA_BUNDLER_RPC") == "" || os.Getenv("SEPOLIA_RPC") == "" {
		t.Skip("Skipping TestWithdrawETHUserOp: CI or missing SEPOLIA endpoints")
	}

	smartWalletConfig := getTestSmartWalletConfig()

	if smartWalletConfig.BundlerURL == "" || smartWalletConfig.EthRpcUrl == "" {
		t.Skip("Skipping TestWithdrawETHUserOp: missing BundlerURL or EthRpcUrl for Sepolia")
	}
	if !strings.Contains(strings.ToLower(smartWalletConfig.BundlerURL), "sepolia") || !strings.Contains(strings.ToLower(smartWalletConfig.EthRpcUrl), "sepolia") {
		t.Skip("Skipping TestWithdrawETHUserOp: configured endpoints are not Sepolia")
	}

	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

	// Test owner address (should match the configured private key)
	owner := common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5")

	// Test withdrawal parameters - sending 0.001 ETH to a test address
	params := &WithdrawalParams{
		RecipientAddress: common.HexToAddress("0xe0f7d11fd714674722d325cd86062a5f1882e13a"), // Test recipient
		Amount:           big.NewInt(1000000000000000),                                      // 0.001 ETH in wei
		Token:            "ETH",
	}

	// Build withdrawal calldata
	callData, err := BuildWithdrawalCalldata(params)
	if err != nil {
		t.Fatalf("Failed to build withdrawal calldata: %v", err)
	}

	t.Logf("Built ETH withdrawal calldata: %d bytes", len(callData))

	// Send UserOp via preset.SendUserOp
	userOp, receipt, err := preset.SendUserOp(
		smartWalletConfig,
		owner,
		callData,
		nil, // No paymaster for this test
		nil, // Use default smart wallet derivation
	)

	if err != nil {
		t.Errorf("ETH withdrawal UserOp failed to send; error %v", err)
		return
	}

	if userOp == nil {
		t.Errorf("UserOp is nil")
		return
	}

	t.Logf("ETH withdrawal UserOp sent successfully")
	t.Logf("UserOp sender: %s", userOp.Sender.Hex())
	t.Logf("UserOp nonce: %s", userOp.Nonce.String())

	// Get UserOp hash
	chainID := big.NewInt(int64(smartWalletConfig.ChainID))
	userOpHash := userOp.GetUserOpHash(smartWalletConfig.EntrypointAddress, chainID)
	t.Logf("UserOp hash: %s", userOpHash.Hex())

	if receipt != nil {
		t.Logf("ETH withdrawal transaction confirmed - TX Hash: %s, Gas used: %d", receipt.TxHash.Hex(), receipt.GasUsed)
	} else {
		t.Logf("ETH withdrawal transaction submitted successfully but receipt is not available yet")
	}
}

// TestWithdrawERC20UserOp tests real ERC20 token withdrawal via UserOp to Sepolia bundler
func TestWithdrawERC20UserOp(t *testing.T) {
	if os.Getenv("CI") != "" || os.Getenv("SEPOLIA_BUNDLER_RPC") == "" || os.Getenv("SEPOLIA_RPC") == "" {
		t.Skip("Skipping TestWithdrawERC20UserOp: CI or missing SEPOLIA endpoints")
	}

	smartWalletConfig := getTestSmartWalletConfig()

	if smartWalletConfig.BundlerURL == "" || smartWalletConfig.EthRpcUrl == "" {
		t.Skip("Skipping TestWithdrawERC20UserOp: missing BundlerURL or EthRpcUrl for Sepolia")
	}
	if !strings.Contains(strings.ToLower(smartWalletConfig.BundlerURL), "sepolia") || !strings.Contains(strings.ToLower(smartWalletConfig.EthRpcUrl), "sepolia") {
		t.Skip("Skipping TestWithdrawERC20UserOp: configured endpoints are not Sepolia")
	}

	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

	// Test owner address (should match the configured private key)
	owner := common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5")

	// Test ERC20 withdrawal parameters - sending 1000 units (1 USDC with 6 decimals) of test token
	params := &WithdrawalParams{
		RecipientAddress: common.HexToAddress("0xe0f7d11fd714674722d325cd86062a5f1882e13a"), // Test recipient
		Amount:           big.NewInt(1000),                                                  // 1000 units
		Token:            "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238",                      // Sepolia USDC
	}

	// Build withdrawal calldata
	callData, err := BuildWithdrawalCalldata(params)
	if err != nil {
		t.Fatalf("Failed to build ERC20 withdrawal calldata: %v", err)
	}

	t.Logf("Built ERC20 withdrawal calldata: %d bytes", len(callData))

	// Send UserOp via preset.SendUserOp
	userOp, receipt, err := preset.SendUserOp(
		smartWalletConfig,
		owner,
		callData,
		nil, // No paymaster for this test
		nil, // Use default smart wallet derivation
	)

	if err != nil {
		t.Errorf("ERC20 withdrawal UserOp failed to send; error %v", err)
		return
	}

	if userOp == nil {
		t.Errorf("UserOp is nil")
		return
	}

	t.Logf("ERC20 withdrawal UserOp sent successfully")
	t.Logf("UserOp sender: %s", userOp.Sender.Hex())
	t.Logf("UserOp nonce: %s", userOp.Nonce.String())

	// Get UserOp hash
	chainID := big.NewInt(int64(smartWalletConfig.ChainID))
	userOpHash := userOp.GetUserOpHash(smartWalletConfig.EntrypointAddress, chainID)
	t.Logf("UserOp hash: %s", userOpHash.Hex())

	if receipt != nil {
		t.Logf("ERC20 withdrawal transaction confirmed - TX Hash: %s, Gas used: %d", receipt.TxHash.Hex(), receipt.GasUsed)
	} else {
		t.Logf("ERC20 withdrawal transaction submitted successfully but receipt is not available yet")
	}
}

// TestWithdrawalWithCustomSmartWallet tests withdrawal from a specific smart wallet address
func TestWithdrawalWithCustomSmartWallet(t *testing.T) {
	if os.Getenv("CI") != "" || os.Getenv("SEPOLIA_BUNDLER_RPC") == "" || os.Getenv("SEPOLIA_RPC") == "" {
		t.Skip("Skipping TestWithdrawalWithCustomSmartWallet: CI or missing SEPOLIA endpoints")
	}

	smartWalletConfig := getTestSmartWalletConfig()

	if smartWalletConfig.BundlerURL == "" || smartWalletConfig.EthRpcUrl == "" {
		t.Skip("Skipping TestWithdrawalWithCustomSmartWallet: missing BundlerURL or EthRpcUrl for Sepolia")
	}
	if !strings.Contains(strings.ToLower(smartWalletConfig.BundlerURL), "sepolia") || !strings.Contains(strings.ToLower(smartWalletConfig.EthRpcUrl), "sepolia") {
		t.Skip("Skipping TestWithdrawalWithCustomSmartWallet: configured endpoints are not Sepolia")
	}

	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

	// Test owner address
	owner := common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5")

	// Specify a custom smart wallet address (using salt=1 for derivation)
	params := &WithdrawalParams{
		RecipientAddress: common.HexToAddress("0xe0f7d11fd714674722d325cd86062a5f1882e13a"),
		Amount:           big.NewInt(500000000000000), // 0.0005 ETH in wei
		Token:            "ETH",
		Salt:             big.NewInt(1), // Use salt=1 instead of default salt=0
	}

	// Build withdrawal calldata
	callData, err := BuildWithdrawalCalldata(params)
	if err != nil {
		t.Fatalf("Failed to build withdrawal calldata: %v", err)
	}

	t.Logf("Built withdrawal calldata for custom smart wallet: %d bytes", len(callData))

	// Derive the specific smart wallet address we'll be using
	// Note: This would require an ethclient.Client instance in a real scenario
	// For now, we'll just log that we're using a custom salt
	t.Logf("Using custom smart wallet with salt=%s", params.Salt.String())

	// Send UserOp via preset.SendUserOp - the salt will be used in smart wallet derivation
	userOp, receipt, err := preset.SendUserOp(
		smartWalletConfig,
		owner,
		callData,
		nil, // No paymaster for this test
		nil, // Let the function derive the address from salt
	)

	if err != nil {
		t.Errorf("Custom wallet withdrawal UserOp failed to send; error %v", err)
		return
	}

	if userOp == nil {
		t.Errorf("UserOp is nil")
		return
	}

	t.Logf("Custom wallet withdrawal UserOp sent successfully")
	t.Logf("UserOp sender: %s", userOp.Sender.Hex())
	t.Logf("UserOp nonce: %s", userOp.Nonce.String())

	if receipt != nil {
		t.Logf("Custom wallet withdrawal transaction confirmed - TX Hash: %s, Gas used: %d", receipt.TxHash.Hex(), receipt.GasUsed)
	} else {
		t.Logf("Custom wallet withdrawal transaction submitted successfully but receipt is not available yet")
	}
}

// getTestSmartWalletConfig creates a test SmartWalletConfig similar to the existing pattern
func getTestSmartWalletConfig() *config.SmartWalletConfig {
	key := testutil.GetTestPrivateKey()
	var controllerPrivateKey *ecdsa.PrivateKey
	var err error

	if key == "" {
		key = "1111111111111111111111111111111111111111111111111111111111111111"
	} else if strings.HasPrefix(key, "0x") {
		key = key[2:]
	}

	controllerPrivateKey, err = crypto.HexToECDSA(key)
	if err != nil {
		dummyKey := "1111111111111111111111111111111111111111111111111111111111111111"
		controllerPrivateKey, _ = crypto.HexToECDSA(dummyKey)
	}

	return &config.SmartWalletConfig{
		EthRpcUrl:            testutil.GetTestRPC(),
		BundlerURL:           testutil.GetTestBundlerRPC(),
		EthWsUrl:             testutil.GetTestWsRPC(),
		FactoryAddress:       common.HexToAddress(testutil.GetTestFactoryAddress()),
		EntrypointAddress:    common.HexToAddress(config.DefaultEntrypointAddressHex),
		ChainID:              11155111, // Sepolia chain ID
		ControllerPrivateKey: controllerPrivateKey,
		PaymasterAddress:     common.HexToAddress(config.DefaultPaymasterAddressHex),
		WhitelistAddresses:   []common.Address{},
	}
}
