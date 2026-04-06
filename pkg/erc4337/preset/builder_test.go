package preset

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa/paymaster"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/bundler"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/stretchr/testify/require"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/ethclient/gethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

func mockGetBaseTestSmartWalletConfig() *config.SmartWalletConfig {
	smartWalletConfig := testutil.GetTestSmartWalletConfig()

	// Derive controller address from private key when available
	if smartWalletConfig.ControllerPrivateKey != nil {
		smartWalletConfig.ControllerAddress = crypto.PubkeyToAddress(smartWalletConfig.ControllerPrivateKey.PublicKey)
	}

	return smartWalletConfig
}

func TestSendUserOp(t *testing.T) {
	smartWalletConfig := mockGetBaseTestSmartWalletConfig()

	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

	ownerAddr, ok := testutil.MustGetTestOwnerAddress()
	if !ok {
		t.Fatal("OWNER_EOA or TEST_PRIVATE_KEY environment variable not set")
	}
	owner := *ownerAddr

	client, err := ethclient.Dial(smartWalletConfig.EthRpcUrl)
	require.NoError(t, err, "Failed to connect to RPC")
	defer client.Close()

	// Ensure wallet is deployed with the current controller key
	controllerPrivateKey := testutil.GetTestControllerPrivateKey()
	salt := big.NewInt(0)
	err = testutil.EnsureWalletDeployed(client, smartWalletConfig.FactoryAddress, owner, salt, controllerPrivateKey)
	require.NoError(t, err, "Failed to ensure wallet is deployed")

	calldata, err := aa.PackExecute(
		common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"), // Sepolia USDC
		big.NewInt(0),
		// Transfer 1000 units of USDC to test destination
		common.FromHex("0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a00000000000000000000000000000000000000000000000000000000000003e8"),
	)

	require.NoError(t, err, "Failed to pack execute calldata")

	paymasterRequest := GetVerifyingPaymasterRequestForDuration(
		smartWalletConfig.PaymasterAddress,
		15*time.Minute,
	)

	userOp, receipt, err := SendUserOp(smartWalletConfig, owner, calldata, paymasterRequest, nil, nil, nil, nil)
	require.NoError(t, err, "UserOp failed to send")
	require.NotNil(t, userOp, "UserOp should not be nil")
	if receipt == nil {
		t.Skip("UserOp sent but receipt not available (confirmation timeout)")
	}

	t.Logf("Transaction executed successfully. TX Hash: %s Gas used: %d", receipt.TxHash.Hex(), receipt.GasUsed)
}

/*
func TestPaymaster(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping test in CI environment")
	}

	smartWalletConfig := mockGetBaseTestSmartWalletConfig()

	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

	// Because we used the master key to signed, the address cannot be calculated from that key and need to set explicitly
	owner := common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5")

	calldata, err := aa.PackExecute(
		common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"), // Sepolia USDC
		big.NewInt(0),
		common.FromHex("0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a00000000000000000000000000000000000000000000000000000000000003e8"),
	)

	if err != nil {
		t.Errorf("expect pack userop successfully but got error: %v", err)
	}

	paymasterRequest := GetVerifyingPaymasterRequestForDuration(smartWalletConfig.PaymasterAddress, 15*time.Minute)
	t.Logf("paymasterAddress: %s validUntil: %s validAfter: %s", smartWalletConfig.PaymasterAddress.Hex(), paymasterRequest.ValidUntil.String(), paymasterRequest.ValidAfter.String())

	// Use the new helper function to build and send the user operation with paymaster
	userOp, receipt, err := SendUserOp(
		smartWalletConfig,
		owner,
		calldata,
		paymasterRequest,
		nil, // senderOverride
		nil, // saltOverride
		nil, // executionFeeWei
		nil, // logger
	)

	if err != nil {
		t.Errorf("Failed to send user operation with paymaster: %v", err)
		return
	}

	if userOp == nil {
		t.Errorf("UserOp is nil")
		return
	}

	// Log the result
	t.Logf("PaymasterAndData: 0x%x", userOp.PaymasterAndData)

	if receipt != nil {
		t.Logf("Transaction executed successfully. TX Hash: %s Gas used: %d", receipt.TxHash.Hex(), receipt.GasUsed)
	} else {
		t.Logf("UserOp submitted but receipt not available yet")
	}
}
*/

// Fix 2: Use SetString but handle the second return value
func mustBigInt(s string, base int) *big.Int {
	val, success := new(big.Int).SetString(s, base)
	if !success {
		panic(fmt.Sprintf("Failed to parse %s as big.Int", s))
	}
	return val
}

func TestBuildUserOpWithPaymasterErrors(t *testing.T) {
	smartWalletConfig := mockGetBaseTestSmartWalletConfig()

	client, err := ethclient.Dial(smartWalletConfig.EthRpcUrl)
	if err != nil {
		t.Fatalf("Failed to connect to the client: %v", err)
	}
	defer client.Close()

	rpcClient, err := rpc.Dial(smartWalletConfig.BundlerURL)
	if err != nil {
		t.Fatalf("Failed to connect to the bundler RPC: %v", err)
	}
	defer rpcClient.Close()

	bundlerClient, err := bundler.NewBundlerClient(smartWalletConfig.BundlerURL)
	if err != nil {
		t.Fatalf("Failed to connect to the bundler: %v", err)
	}

	invalidPaymasterAddress := common.HexToAddress("0x0000000000000000000000000000000000000000")
	ownerAddr, ok := testutil.MustGetTestOwnerAddress()
	if !ok {
		t.Fatal("OWNER_EOA or TEST_PRIVATE_KEY environment variable not set")
	}
	owner := *ownerAddr
	validUntil := big.NewInt(0)
	validAfter := big.NewInt(0)

	calldata, err := aa.PackExecute(
		common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"), // Sepolia USDC
		big.NewInt(0),
		common.FromHex("0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e8"),
	)
	if err != nil {
		t.Errorf("failed to pack execute: %v", err)
	}

	_, err = BuildUserOpWithPaymaster(
		smartWalletConfig,
		client,
		bundlerClient,
		owner,
		calldata,
		invalidPaymasterAddress,
		validUntil,
		validAfter,
		nil, // senderOverride
		nil, // saltOverride
		nil, // nonceOverride - let it fetch from chain
		nil, // callGasOverride
		nil, // verificationGasOverride
		nil, // preVerificationGasOverride
		nil, // logger
	)

	if err == nil {
		t.Errorf("Expected error when initializing PayMaster contract with invalid address, but got nil")
	}
}

func TestPaymasterTimeValidation(t *testing.T) {
	smartWalletConfig := mockGetBaseTestSmartWalletConfig()

	client, err := ethclient.Dial(smartWalletConfig.EthRpcUrl)
	if err != nil {
		t.Fatalf("Failed to connect to the client: %v", err)
	}
	defer client.Close()

	rpcClient, err := rpc.Dial(smartWalletConfig.EthRpcUrl)
	if err != nil {
		t.Fatalf("Failed to connect to the client: %v", err)
	}
	defer rpcClient.Close()

	gethClient := gethclient.New(rpcClient)
	_ = gethClient // Avoid unused variable warning

	paymasterContract, err := paymaster.NewPayMaster(smartWalletConfig.PaymasterAddress, client)
	if err != nil {
		t.Fatalf("Failed to initialize PayMaster contract: %v", err)
	}

	userOp := userop.UserOperation{
		Sender:               common.HexToAddress("0x5afb1b1bc212c6417c575a78bf9921cc05f6d3ed"),
		Nonce:                big.NewInt(15),
		InitCode:             common.FromHex("0x"),
		CallData:             common.FromHex("0xb61d27f600000000000000000000000003"),
		CallGasLimit:         mustBigInt("989680", 16),
		VerificationGasLimit: mustBigInt("989680", 16),
		PreVerificationGas:   mustBigInt("989680", 16),
		MaxFeePerGas:         mustBigInt("1140f2", 16),
		MaxPriorityFeePerGas: mustBigInt("113e10", 16),
		PaymasterAndData:     common.FromHex("0xffffff"),
		Signature:            common.FromHex("0x1234"),
	}

	paymasterUserOp := paymaster.UserOperation{
		Sender:               userOp.Sender,
		Nonce:                userOp.Nonce,
		InitCode:             userOp.InitCode,
		CallData:             userOp.CallData,
		CallGasLimit:         userOp.CallGasLimit,
		VerificationGasLimit: userOp.VerificationGasLimit,
		PreVerificationGas:   userOp.PreVerificationGas,
		MaxFeePerGas:         userOp.MaxFeePerGas,
		MaxPriorityFeePerGas: userOp.MaxPriorityFeePerGas,
		PaymasterAndData:     userOp.PaymasterAndData,
		Signature:            userOp.Signature,
	}

	testCases := []struct {
		name       string
		validUntil *big.Int
		validAfter *big.Int
		expectErr  bool
	}{
		{
			name:       "Valid time range",
			validUntil: big.NewInt(time.Now().Unix() + 3600),
			validAfter: big.NewInt(time.Now().Unix()),
			expectErr:  false,
		},
		{
			name:       "validUntil in the past",
			validUntil: big.NewInt(time.Now().Unix() - 3600),
			validAfter: big.NewInt(time.Now().Unix() - 7200),
			expectErr:  false, // Contract may not validate this at hash generation
		},
		{
			name:       "validAfter in the future",
			validUntil: big.NewInt(time.Now().Unix() + 7200),
			validAfter: big.NewInt(time.Now().Unix() + 3600),
			expectErr:  false, // Contract may not validate this at hash generation
		},
		{
			name:       "validUntil before validAfter",
			validUntil: big.NewInt(time.Now().Unix()),
			validAfter: big.NewInt(time.Now().Unix() + 3600),
			expectErr:  false, // Contract may not validate this at hash generation
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Get hash from PayMaster contract
			hash, err := paymasterContract.GetHash(nil, paymasterUserOp, tc.validUntil, tc.validAfter)

			if tc.expectErr && err == nil {
				t.Errorf("Expected error but got nil for case %s", tc.name)
			} else if !tc.expectErr && err != nil {
				t.Errorf("Expected no error but got %v for case %s", err, tc.name)
			}

			t.Logf("Hash for case %s: %s", tc.name, common.Bytes2Hex(hash[:]))
		})
	}
}

func callValidatePaymasterUserOp(t *testing.T, paymasterContract *paymaster.PayMaster, userOp *userop.UserOperation, chainID *big.Int) ([]byte, *big.Int, error) {
	smartWalletConfig := mockGetBaseTestSmartWalletConfig()
	client, err := ethclient.Dial(smartWalletConfig.EthRpcUrl)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to connect to Ethereum client: %w", err)
	}
	defer client.Close()

	paymasterAddress := smartWalletConfig.PaymasterAddress

	// Convert to paymaster.UserOperation
	paymasterUserOp := paymaster.UserOperation{
		Sender:               userOp.Sender,
		Nonce:                userOp.Nonce,
		InitCode:             userOp.InitCode,
		CallData:             userOp.CallData,
		CallGasLimit:         userOp.CallGasLimit,
		VerificationGasLimit: userOp.VerificationGasLimit,
		PreVerificationGas:   userOp.PreVerificationGas,
		MaxFeePerGas:         userOp.MaxFeePerGas,
		MaxPriorityFeePerGas: userOp.MaxPriorityFeePerGas,
		PaymasterAndData:     userOp.PaymasterAndData,
		Signature:            userOp.Signature,
	}

	userOpHash := userOp.GetUserOpHash(aa.EntrypointAddress, chainID)

	paymasterABI, err := abi.JSON(strings.NewReader(paymaster.PayMasterMetaData.ABI))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse paymaster ABI: %w", err)
	}

	entryPointAddress, err := paymasterContract.EntryPoint(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get EntryPoint address: %w", err)
	}

	maxCost := big.NewInt(1e18) // 1 ETH max cost - arbitrary for test
	callData, err := paymasterABI.Pack("validatePaymasterUserOp", paymasterUserOp, userOpHash, maxCost)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to pack validatePaymasterUserOp call: %w", err)
	}

	msg := ethereum.CallMsg{
		From: entryPointAddress,
		To:   &paymasterAddress,
		Data: callData,
	}

	result, err := client.CallContract(context.Background(), msg, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("contract call failed: %w", err)
	}

	// Unpack the result
	outputs, err := paymasterABI.Unpack("validatePaymasterUserOp", result)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to unpack result: %w", err)
	}

	context := outputs[0].([]byte)
	validationData := outputs[1].(*big.Int)

	return context, validationData, nil
}
