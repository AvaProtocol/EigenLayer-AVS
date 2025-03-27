package preset

import (
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/AvaProtocol/ap-avs/core/chainio/aa"
	"github.com/AvaProtocol/ap-avs/core/testutil"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/AvaProtocol/ap-avs/pkg/erc4337/userop"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/AvaProtocol/ap-avs/core/chainio/aa/paymaster"
	//"github.com/ethereum/go-ethereum/common/hexutil"
)

func getControllerSigner() *ecdsa.PrivateKey {
	key := os.Getenv("CONTROLLER_PRIVATE_KEY")

	if key[0:2] == "0x" {
		key = key[2:]
	}

	privateKey, err := crypto.HexToECDSA(key)
	if err != nil {
		panic(err)
	}

	return privateKey
}

func TestSendUserOp(t *testing.T) {
	smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
	smartWalletConfig.BundlerURL = "http://localhost:3437/rpc"

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
		// Transfering of 0.00761 the test token
		// Example result on base sepolia:
		// https://sepolia.basescan.org/tx/0x812290f4a588cb62bd4a46698ece51d576a75729af5dda497badb0ef8f8cddfa
		// https://sepolia.basescan.org/tx/0xef607557e727ae1602c6e74a625cffc57aa7108c4d470d38b96cfd4539ee978f
		//common.HexToAddress("0x0a0c037267a690e9792f4660c29989babec9cffb"),
		common.HexToAddress("0x036cbd53842c5426634e7929541ec2318f3dcf7e"), // base sepolia usdc
		big.NewInt(0),
		common.FromHex("0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e80000000000000000000000000000000000000000000000000000000"),
		// common.FromHex("0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000001b125981304000"),
		//common.FromHex("0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000001b134255d55000"),
	)

	if err != nil {
		t.Errorf("expect pack userop succesfully but got error: %v", err)
	}

	userop, receipt, err := SendUserOp(smartWalletConfig, owner, calldata, nil)
	if err != nil || userop == nil {
		t.Errorf("UserOp failed to send; error %v", err)
	}

	if err != nil {
		a, _ := json.Marshal(receipt)
		b, _ := json.Marshal(userop)
		//t.Logf("UserOp submit succesfully. tx: %s userop: %s", a, b)
		t.Logf("UserOp submit failed. userop: %s tx: %s err: %v", a, b, err)
	}
	t.Logf("Transaction executed successfully. TX Hash: %s Gas used: %d", receipt.TxHash.Hex(), receipt.GasUsed)
}

func TestPaymaster(t *testing.T) {
	smartWalletConfig := testutil.GetBaseTestSmartWalletConfig()
	smartWalletConfig.BundlerURL = "http://localhost:3437/rpc"

	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)


	// Because we used the master key to signed, the address cannot be calculated from that key and need to set explicitly
	owner := common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5")

	calldata, err := aa.PackExecute(
		common.HexToAddress("0x036cbd53842c5426634e7929541ec2318f3dcf7e"), // base sepolia usdc
		big.NewInt(0),
		common.FromHex("0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e80000000000000000000000000000000000000000000000000000000"),
	)

	if err != nil {
		t.Errorf("expect pack userop succesfully but got error: %v", err)
	}
	
	// Create validUntil and validAfter values (1 hour from now and current time)
	currentTime := time.Now().Unix()
	validAfter := big.NewInt(currentTime)
	validUntil := big.NewInt(currentTime + 3600) // Valid for 1 hour
	
	t.Logf("paymasterAddress: %s validUntil: %s validAfter: %s", smartWalletConfig.PaymasterAddress.Hex(), validUntil.String(), validAfter.String())
	
	// Use the new helper function to build and send the user operation with paymaster
	userOp, receipt, err := SendUserOp(
		smartWalletConfig,
		owner,
		calldata,
		&VerifyingPaymasterRequest{
			PaymasterAddress: smartWalletConfig.PaymasterAddress,
			ValidUntil:       validUntil,
			ValidAfter:       validAfter,
		},
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

func TestGetHash(t *testing.T) {
	nonce := new(big.Int)
	nonce.SetString("15", 16)

	userOp := userop.UserOperation{
		// GetHash on VerifyingPaymaster contract pack its own nonce to prevent re-use of nonce so we use a dummy address in this test to simulate the correctness of logic of our GetHash function. Especially around pasing the dumy PaymasterAndData and Signature
		Sender:              common.HexToAddress("0x5afb1b1bc212c6417c575a78bf9921cc05f6d3ed"),
		Nonce:               nonce,
		InitCode:            common.FromHex("0x"),
		CallData:            common.FromHex("0xb61d27f6000000000000000000000000036cbd53842c5426634e7929541ec2318f3dcf7e000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000600000000000000000000000000000000000000000000000000000000000000060a9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000000000000000003e80000000000000000000000000000000000000000000000000000000"),
		
		CallGasLimit:         mustBigInt("989680", 16),
		VerificationGasLimit: mustBigInt("989680", 16),
		PreVerificationGas:   mustBigInt("989680", 16),
		MaxFeePerGas:         mustBigInt("1140f2", 16),
		MaxPriorityFeePerGas: mustBigInt("113e10", 16),
		PaymasterAndData:    common.FromHex("0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"),
		Signature:           common.FromHex("0x1234567890abcdef"),
	}

	// Setup test client
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

	if common.Bytes2Hex(hash[:]) != "fb00da1521ff234a7e82fc43ef8c1fd98bb1ce2c780e2c3d0d908b0be5c7212d" {
		t.Fatalf("Expected hash to be fb00da1521ff234a7e82fc43ef8c1fd98bb1ce2c780e2c3d0d908b0be5c7212d, got %s", common.Bytes2Hex(hash[:]))
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