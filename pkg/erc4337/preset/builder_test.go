package preset

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/assert"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa/signer"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/bundler"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/paymaster"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient/gethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

// Dummy placeholder for PaymasterAndData used in tests
// This is a placeholder value that will be replaced with actual paymaster data in the tests
const dummyPaymasterAndDataHex = "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"

func getControllerSigner(t *testing.T, privateKey *ecdsa.PrivateKey) *signer.ControllerSigner {
	controllerSigner, err := signer.NewControllerSigner(privateKey)
	if err != nil {
		t.Fatalf("Failed to create controller signer: %v", err)
	}
	return controllerSigner
}

func TestSendUserOp(t *testing.T) {
	smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
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

	bundlerClient, err := bundler.NewClient(smartWalletConfig.BundlerRpcUrl)
	if err != nil {
		t.Fatalf("Failed to connect to the bundler: %v", err)
	}
	defer bundlerClient.Close()

	owner := common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5")

	calldata, err := aa.PackExecute(
		common.HexToAddress("0x036cbd53842c5426634e7929541ec2318f3dcf7e"),
		big.NewInt(0),
		common.FromHex("0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e8"),
	)
	if err != nil {
		t.Fatalf("Failed to pack execute: %v", err)
	}

	paymasterReq := GetVerifyingPaymasterRequestForDuration(smartWalletConfig.PaymasterAddress, 15*time.Minute)

	_, _, err = SendUserOp(
		smartWalletConfig,
		owner,
		calldata,
		paymasterReq,
	)

	if err != nil && strings.Contains(err.Error(), "websocket") {
		t.Logf("Test skipped: SendUserOp could not be completed due to websocket connection issues: %v", err)
		return
	}

	if err != nil {
		t.Errorf("expected SendUserOp successful but got error: %v", err)
	}
}

func TestPaymaster(t *testing.T) {
	smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
	client, err := ethclient.Dial(smartWalletConfig.EthRpcUrl)
	if err != nil {
		t.Fatalf("Failed to connect to the client: %v", err)
	}
	defer client.Close()

	paymasterContract, err := paymaster.NewPayMaster(smartWalletConfig.PaymasterAddress, client)
	if err != nil {
		t.Fatalf("Failed to initialize PayMaster contract: %v", err)
	}

	owner, err := paymasterContract.Owner(&bind.CallOpts{})
	if err != nil {
		t.Fatalf("Failed to get owner: %v", err)
	}

	t.Logf("PayMaster owner: %s", owner.Hex())

	entryPoint, err := paymasterContract.EntryPoint(&bind.CallOpts{})
	if err != nil {
		t.Fatalf("Failed to get entry point: %v", err)
	}

	t.Logf("PayMaster entry point: %s", entryPoint.Hex())

	signer, err := paymasterContract.Signer(&bind.CallOpts{})
	if err != nil {
		t.Fatalf("Failed to get signer: %v", err)
	}

	t.Logf("PayMaster signer: %s", signer.Hex())

	// Verify the signer is the controller address
	controllerAddress := crypto.PubkeyToAddress(smartWalletConfig.ControllerPrivateKey.PublicKey)
	if signer != controllerAddress {
		t.Errorf("Expected signer to be %s, got %s", controllerAddress.Hex(), signer.Hex())
	}
}

func TestGetHash(t *testing.T) {
	smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
	client, err := ethclient.Dial(smartWalletConfig.EthRpcUrl)
	if err != nil {
		t.Fatalf("Failed to connect to the client: %v", err)
	}
	defer client.Close()

	paymasterContract, err := paymaster.NewPayMaster(smartWalletConfig.PaymasterAddress, client)
	if err != nil {
		t.Fatalf("Failed to initialize PayMaster contract: %v", err)
	}

	testCases := []struct {
		name       string
		validUntil *big.Int
		validAfter *big.Int
	}{
		{
			name:       "Valid for 1 hour",
			validUntil: big.NewInt(time.Now().Unix() + 3600),
			validAfter: big.NewInt(time.Now().Unix()),
		},
		{
			name:       "Valid for 1 day",
			validUntil: big.NewInt(time.Now().Unix() + 86400),
			validAfter: big.NewInt(time.Now().Unix()),
		},
		{
			name:       "Valid for 1 week",
			validUntil: big.NewInt(time.Now().Unix() + 604800),
			validAfter: big.NewInt(time.Now().Unix()),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			userOp := paymaster.UserOperation{
				Sender:               common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5"),
				Nonce:                big.NewInt(0),
				InitCode:             []byte{},
				CallData:             []byte{},
				CallGasLimit:         big.NewInt(200000),
				VerificationGasLimit: big.NewInt(200000),
				PreVerificationGas:   big.NewInt(200000),
				MaxFeePerGas:         big.NewInt(1000000000),
				MaxPriorityFeePerGas: big.NewInt(1000000000),
				PaymasterAndData:     []byte{},
				Signature:            []byte{},
			}

			hash, err := paymasterContract.GetHash(nil, userOp, tc.validUntil, tc.validAfter)
			if err != nil {
				t.Fatalf("Failed to get hash: %v", err)
			}

			t.Logf("Hash for case %s: %s", tc.name, common.Bytes2Hex(hash[:]))
		})
	}
}

func mustBigInt(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic(fmt.Sprintf("invalid big int: %s", s))
	}
	return n
}

func TestBuildUserOpWithPaymasterErrors(t *testing.T) {
	smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
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

	bundlerClient, err := bundler.NewClient(smartWalletConfig.BundlerRpcUrl)
	if err != nil {
		t.Fatalf("Failed to connect to the bundler: %v", err)
	}
	defer bundlerClient.Close()

	owner := common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5")

	calldata, err := aa.PackExecute(
		common.HexToAddress("0x036cbd53842c5426634e7929541ec2318f3dcf7e"),
		big.NewInt(0),
		common.FromHex("0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e8"),
	)
	if err != nil {
		t.Fatalf("Failed to pack execute: %v", err)
	}

	// Test with nil paymaster request
	_, _, err = SendUserOp(
		smartWalletConfig,
		owner,
		calldata,
		nil,
	)

	if err != nil && strings.Contains(err.Error(), "websocket") {
		t.Logf("Test skipped: SendUserOp could not be completed due to websocket connection issues: %v", err)
		return
	}

	if err != nil {
		t.Errorf("expected SendUserOp successful but got error: %v", err)
	}
}

func TestPaymasterTimeValidation(t *testing.T) {
	testCases := []struct {
		name        string
		validUntil  *big.Int
		validAfter  *big.Int
		expectError bool
	}{
		{
			name:        "Valid time range",
			validUntil:  big.NewInt(time.Now().Unix() + 3600),
			validAfter:  big.NewInt(time.Now().Unix() - 3600),
			expectError: false,
		},
		{
			name:        "ValidUntil in the past",
			validUntil:  big.NewInt(time.Now().Unix() - 3600),
			validAfter:  big.NewInt(time.Now().Unix() - 7200),
			expectError: true,
		},
		{
			name:        "ValidAfter in the future",
			validUntil:  big.NewInt(time.Now().Unix() + 7200),
			validAfter:  big.NewInt(time.Now().Unix() + 3600),
			expectError: true,
		},
		{
			name:        "ValidUntil before ValidAfter",
			validUntil:  big.NewInt(time.Now().Unix() - 3600),
			validAfter:  big.NewInt(time.Now().Unix() + 3600),
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePaymasterTime(tc.validUntil, tc.validAfter)
			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestValidatePaymasterUserOpSuccess(t *testing.T) {
	smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
	client, err := ethclient.Dial(smartWalletConfig.EthRpcUrl)
	if err != nil {
		t.Fatalf("Failed to connect to the client: %v", err)
	}
	defer client.Close()

	paymasterContract, err := paymaster.NewPayMaster(smartWalletConfig.PaymasterAddress, client)
	if err != nil {
		t.Fatalf("Failed to initialize PayMaster contract: %v", err)
	}

	chainID, err := client.ChainID(context.Background())
	if err != nil {
		t.Fatalf("Failed to get chain ID: %v", err)
	}
	
	owner := common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5")
	
	calldata, err := aa.PackExecute(
		common.HexToAddress("0x036cbd53842c5426634e7929541ec2318f3dcf7e"),
		big.NewInt(0),
		common.FromHex("0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e8"),
	)
	if err != nil {
		t.Fatalf("Failed to pack execute: %v", err)
	}
	
	// Create validUntil and validAfter values (1 hour from now and current time)
	currentTime := time.Now().Unix()
	validAfter := big.NewInt(currentTime)
	validUntil := big.NewInt(currentTime + 3600) // Valid for 1 hour
	
	initCode := []byte{}
	nonce := big.NewInt(0)
	
	userOp := &userop.UserOperation{
		Sender:               owner,
		Nonce:                nonce,
		InitCode:             initCode,
		CallData:             calldata,
		CallGasLimit:         big.NewInt(200000),
		VerificationGasLimit: big.NewInt(200000),
		PreVerificationGas:   big.NewInt(200000),
		MaxFeePerGas:         big.NewInt(1000000000),
		MaxPriorityFeePerGas: big.NewInt(1000000000),
		PaymasterAndData:     []byte{},
		Signature:            []byte{},
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
		PaymasterAndData:     common.FromHex(dummyPaymasterAndDataHex),
		Signature:            common.FromHex("0x1234567890abcdef"),
	}
	
	paymasterHash, err := paymasterContract.GetHash(nil, paymasterUserOp, validUntil, validAfter)
	if err != nil {
		t.Fatalf("Failed to get paymaster hash: %v", err)
	}
	
	paymasterSignature, err := signer.SignMessage(smartWalletConfig.ControllerPrivateKey, paymasterHash[:])
	if err != nil {
		t.Fatalf("Failed to sign paymaster hash: %v", err)
	}
	
	uint48Type, _ := abi.NewType("uint48", "", nil)
	timestampArgs := abi.Arguments{
		{Type: uint48Type},
		{Type: uint48Type},
	}
	
	encodedTimestamps, err := timestampArgs.Pack(
		validUntil,
		validAfter,
	)
	if err != nil {
		t.Fatalf("Failed to ABI encode timestamps: %v", err)
	}
	
	paymasterAndData := append(smartWalletConfig.PaymasterAddress.Bytes(), encodedTimestamps...)
	paymasterAndData = append(paymasterAndData, paymasterSignature...)
	
	userOp.PaymasterAndData = paymasterAndData
	
	userOpHash := userOp.GetUserOpHash(aa.EntrypointAddress, chainID)
	
	userOp.Signature, err = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, userOpHash.Bytes())
	if err != nil {
		t.Fatalf("Failed to sign final UserOp: %v", err)
	}
	
	context, validationData, err := callValidatePaymasterUserOp(t, paymasterContract, userOp, chainID)
	if err != nil {
		t.Fatalf("Failed to validate paymaster userOp: %v", err)
	}
	
	t.Logf("Validation completed - validationData: %s, context length: %d", 
		validationData.String(), len(context))
}

func TestValidatePaymasterUserOpWithInvalidSignature(t *testing.T) {
	smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
	client, err := ethclient.Dial(smartWalletConfig.EthRpcUrl)
	if err != nil {
		t.Fatalf("Failed to connect to the client: %v", err)
	}
	defer client.Close()

	paymasterContract, err := paymaster.NewPayMaster(smartWalletConfig.PaymasterAddress, client)
	if err != nil {
		t.Fatalf("Failed to initialize PayMaster contract: %v", err)
	}

	chainID, err := client.ChainID(context.Background())
	if err != nil {
		t.Fatalf("Failed to get chain ID: %v", err)
	}
	
	owner := common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5")
	
	calldata, err := aa.PackExecute(
		common.HexToAddress("0x036cbd53842c5426634e7929541ec2318f3dcf7e"),
		big.NewInt(0),
		common.FromHex("0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e8"),
	)
	if err != nil {
		t.Fatalf("Failed to pack execute: %v", err)
	}
	
	currentTime := time.Now().Unix()
	validAfter := big.NewInt(currentTime)
	validUntil := big.NewInt(currentTime + 3600) // Valid for 1 hour
	
	initCode := []byte{}
	nonce := big.NewInt(0)
	
	userOp := &userop.UserOperation{
		Sender:               owner,
		Nonce:                nonce,
		InitCode:             initCode,
		CallData:             calldata,
		CallGasLimit:         big.NewInt(200000),
		VerificationGasLimit: big.NewInt(200000),
		PreVerificationGas:   big.NewInt(200000),
		MaxFeePerGas:         big.NewInt(1000000000),
		MaxPriorityFeePerGas: big.NewInt(1000000000),
		PaymasterAndData:     []byte{},
		Signature:            []byte{},
	}
	
	randomKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("Failed to generate random key: %v", err)
	}
	
	tempConfig := *smartWalletConfig
	tempConfig.ControllerPrivateKey = randomKey
	
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
		PaymasterAndData:     common.FromHex(dummyPaymasterAndDataHex),
		Signature:            common.FromHex("0x1234567890abcdef"),
	}
	
	paymasterHash, err := paymasterContract.GetHash(nil, paymasterUserOp, validUntil, validAfter)
	if err != nil {
		t.Fatalf("Failed to get paymaster hash: %v", err)
	}
	
	paymasterSignature, err := signer.SignMessage(randomKey, paymasterHash[:])
	if err != nil {
		t.Fatalf("Failed to sign paymaster hash: %v", err)
	}
	
	uint48Type, _ := abi.NewType("uint48", "", nil)
	timestampArgs := abi.Arguments{
		{Type: uint48Type},
		{Type: uint48Type},
	}
	
	encodedTimestamps, err := timestampArgs.Pack(
		validUntil,
		validAfter,
	)
	if err != nil {
		t.Fatalf("Failed to ABI encode timestamps: %v", err)
	}
	
	paymasterAndData := append(smartWalletConfig.PaymasterAddress.Bytes(), encodedTimestamps...)
	paymasterAndData = append(paymasterAndData, paymasterSignature...)
	
	userOp.PaymasterAndData = paymasterAndData
	
	userOpHash := userOp.GetUserOpHash(aa.EntrypointAddress, chainID)
	
	userOp.Signature, err = signer.SignMessage(smartWalletConfig.ControllerPrivateKey, userOpHash.Bytes())
	if err != nil {
		t.Fatalf("Failed to sign final UserOp: %v", err)
	}
	
	context, validationData, err := callValidatePaymasterUserOp(t, paymasterContract, userOp, chainID)
	if err != nil {
		t.Fatalf("Failed to validate paymaster userOp: %v", err)
	}
	
	if validationData.Cmp(big.NewInt(0)) == 0 {
		t.Errorf("Expected validationData to indicate failure, got 0 (success)")
	}
	
	t.Logf("Validation correctly failed - validationData: %s, context length: %d", 
		validationData.String(), len(context))
}

// callValidatePaymasterUserOp calls the validatePaymasterUserOp method directly and returns the resulting context and validationData
func callValidatePaymasterUserOp(t *testing.T, paymasterContract *paymaster.PayMaster, userOp *userop.UserOperation, chainID *big.Int) ([]byte, *big.Int, error) {
	smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
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
	
	// Create userOpHash
	userOpHash := userOp.GetUserOpHash(aa.EntrypointAddress, chainID)
	
	// Need to create ABI to call the function directly
	paymasterABI, err := abi.JSON(strings.NewReader(paymaster.PayMasterMetaData.ABI))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse paymaster ABI: %w", err)
	}
	
	entryPointAddress, err := paymasterContract.EntryPoint(nil)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get EntryPoint address: %w", err)
	}
	
	// Pack the function call data
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
	
	// The method returns (bytes context, uint256 validationData)
	context := outputs[0].([]byte)
	validationData := outputs[1].(*big.Int)
	
	return context, validationData, nil
}
