package preset

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa/paymaster"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/bundler"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/ethclient/gethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

const dummyPaymasterAndDataHex = "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

func mockGetBaseTestSmartWalletConfig() *config.SmartWalletConfig {
	key := os.Getenv("TEST_PRIVATE_KEY")
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
		EthRpcUrl:            "https://sepolia.base.org",
		BundlerURL:           "https://api.stackup.sh/v1/node/sepolia-bundler",
		EthWsUrl:             "wss://sepolia.base.org",
		FactoryAddress:       common.HexToAddress("0x29adA1b5217242DEaBB142BC3b1bCfFdd56008e7"),
		EntrypointAddress:    common.HexToAddress("0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789"),
		ControllerPrivateKey: controllerPrivateKey,
		PaymasterAddress:     common.HexToAddress("0xB985af5f96EF2722DC99aEBA573520903B86505e"),
		WhitelistAddresses:   []common.Address{},
	}
}

func TestSendUserOp(t *testing.T) {
	if os.Getenv("CI") != "" {
		t.Skip("Skipping test in CI environment")
	}

	smartWalletConfig := mockGetBaseTestSmartWalletConfig()

	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)

	// Because we used the  master key to signed, the address cannot be calculate from that key and need to set explicitly
	owner := common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5")

	//calldata := common.FromHex("b61d27f600000000000000000000000069256ca54e6296e460dec7b29b7dcd97b81a3d55000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000600000000000000000000000000000000000000000000000000000000000000044a9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a0000000000000000000000000000000000000000000000001bc16d674ec8000000000000000000000000000000000000000000000000000000000000")

	//calldata := common.FromHex("0xb61d27f600000000000000000000000069256ca54e6296e460dec7b29b7dcd97b81a3d55000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000600000000000000000000000000000000000000000000000000000000000000044a9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000001b094132bda00000000000000000000000000000000000000000000000000000000000")

	calldata, err := aa.PackExecute(
		// Sepolia Network example
		// For test token on base sepolia
		// these test token can be minted by anyone to help us run these transfer test
		// This can be any contract/token as long as we have enough token to fund the AA wallet to perform real test
		// Example result on seplia:       https://sepolia.etherscan.io/tx/0xb438f9583a0a505806fa756e1950430e6060c8b4662807c6c40b9f2dec9d726f
		// common.HexToAddress("0x69256ca54e6296e460dec7b29b7dcd97b81a3d55"),
		// big.NewInt(0),
		// common.FromHex("0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a00000000000000000000000000000000000000000000000000000000000003e8"),

		// Base Sepolia Network example
		// Transferring of 0.00761 the test token
		// Example result on base sepolia:
		// https://sepolia.basescan.org/tx/0x812290f4a588cb62bd4a46698ece51d576a75729af5dda497badb0ef8f8cddfa
		// https://sepolia.basescan.org/tx/0xef607557e727ae1602c6e74a625cffc57aa7108c4d470d38b96cfd4539ee978f
		//common.HexToAddress("0x0a0c037267a690e9792f4660c29989babec9cffb"),
		common.HexToAddress("0x036cbd53842c5426634e7929541ec2318f3dcf7e"), // base sepolia usdc
		big.NewInt(0),
		common.FromHex("0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a00000000000000000000000000000000000000000000000000000000000003e8"),
		// common.FromHex("0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000001b125981304000"),
		//common.FromHex("0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000001b134255d55000"),
	)

	if err != nil {
		t.Errorf("expect pack userop successfully but got error: %v", err)
	}

	userop, receipt, err := SendUserOp(smartWalletConfig, owner, calldata, nil)
	if err != nil || userop == nil {
		t.Errorf("UserOp failed to send; error %v", err)
	}

	if err != nil {
		a, _ := json.Marshal(receipt)
		b, _ := json.Marshal(userop)
		//t.Logf("UserOp submit successfully. tx: %s userop: %s", a, b)
		t.Logf("UserOp submit failed. userop: %s tx: %s err: %v", a, b, err)
		return
	}

	if receipt == nil {
		t.Logf("Transaction submitted successfully but receipt is not available yet")
		return
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
		common.HexToAddress("0x036cbd53842c5426634e7929541ec2318f3dcf7e"), // base sepolia usdc
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

func TestGetHash(t *testing.T) {
	nonce := new(big.Int)
	nonce.SetString("15", 16)

	userOp := userop.UserOperation{
		// GetHash on VerifyingPaymaster contract pack its own nonce to prevent re-use of nonce so we use a dummy address in this test to simulate the correctness of logic of our GetHash function. Especially around pasing the dumy PaymasterAndData and Signature
		Sender:   common.HexToAddress("0x5afb1b1bc212c6417c575a78bf9921cc05f6d3ed"),
		Nonce:    nonce,
		InitCode: common.FromHex("0x"),
		CallData: common.FromHex("0xb61d27f6000000000000000000000000036cbd53842c5426634e7929541ec2318f3dcf7e000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000600000000000000000000000000000000000000000000000000000000000000060a9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e80000000000000000000000000000000000000000000000000000000"),

		CallGasLimit:         mustBigInt("989680", 16),
		VerificationGasLimit: mustBigInt("989680", 16),
		PreVerificationGas:   mustBigInt("989680", 16),
		MaxFeePerGas:         mustBigInt("1140f2", 16),
		MaxPriorityFeePerGas: mustBigInt("113e10", 16),
		PaymasterAndData:     common.FromHex("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"),
		Signature:            common.FromHex("0x1234567890abcdef"),
	}

	// Setup test client
	smartWalletConfig := mockGetBaseTestSmartWalletConfig()
	client, err := ethclient.Dial(smartWalletConfig.EthRpcUrl)
	if err != nil {
		t.Fatalf("Failed to connect to the client: %v", err)
	}
	defer client.Close()

	paymasterContract, err := paymaster.NewPayMaster(smartWalletConfig.PaymasterAddress, client)
	if err != nil {
		t.Fatalf("Failed to initialize PayMaster contract: %v", err)
	}

	// Convert UserOperation to PayMaster.UserOperation
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

	// Set validUntil and validAfter
	validUntil := big.NewInt(0x67e536a2)
	validAfter := big.NewInt(0x67e52892)

	// Get hash from PayMaster contract
	hash, err := paymasterContract.GetHash(nil, paymasterUserOp, validUntil, validAfter)
	if err != nil {
		t.Fatalf("Failed to get hash from PayMaster contract: %v", err)
	}

	if common.Bytes2Hex(hash[:]) != "14972f699106bae44f682fd688b936dc1efce4be3b3bdd838521ac385ca5acc7" {
		t.Fatalf("Expected hash to be 14972f699106bae44f682fd688b936dc1efce4be3b3bdd838521ac385ca5acc7, got %s", common.Bytes2Hex(hash[:]))
	}
}

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
	owner := common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5")
	validUntil := big.NewInt(0)
	validAfter := big.NewInt(0)

	calldata, err := aa.PackExecute(
		common.HexToAddress("0x036cbd53842c5426634e7929541ec2318f3dcf7e"),
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
