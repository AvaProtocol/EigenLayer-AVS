package preset

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"
	"testing"

	"github.com/AvaProtocol/ap-avs/core/chainio/aa"
	"github.com/AvaProtocol/ap-avs/core/testutil"
	"github.com/AvaProtocol/ap-avs/pkg/erc4337/bundler"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
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
	smartWalletConfig := testutil.GetTestSmartWalletConfig()
	aa.SetFactoryAddress(smartWalletConfig.FactoryAddress)
	fmt.Println(smartWalletConfig)

	conn, _ := ethclient.Dial(smartWalletConfig.EthRpcUrl)
	defer func() {
		conn.Close()
	}()
	// Because we used the  master key to signed, the address cannot be calculate from that key and need to set explicitly
	owner := common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5")
	bundlerClient, e := bundler.NewBundlerClient(smartWalletConfig.BundlerURL)
	if e != nil {
		panic(e)
	}

	//calldata := common.FromHex("b61d27f600000000000000000000000069256ca54e6296e460dec7b29b7dcd97b81a3d55000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000600000000000000000000000000000000000000000000000000000000000000044a9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a0000000000000000000000000000000000000000000000001bc16d674ec8000000000000000000000000000000000000000000000000000000000000")

	//calldata := common.FromHex("0xb61d27f600000000000000000000000069256ca54e6296e460dec7b29b7dcd97b81a3d55000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000600000000000000000000000000000000000000000000000000000000000000044a9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000001b094132bda00000000000000000000000000000000000000000000000000000000000")

	calldata, e := aa.PackExecute(
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
		common.HexToAddress("0x0a0c037267a690e9792f4660c29989babec9cffb"),
		big.NewInt(0),
		common.FromHex("0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000001b125981304000"),
		//common.FromHex("0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000001b134255d55000"),
	)

	txResult, err := SendUserOp(conn, bundlerClient, smartWalletConfig.ControllerPrivateKey, owner, calldata)
	if err != nil || txResult == "" {
		t.Errorf("UserOp failed to send; error %v", err)
	}

	t.Logf("UserOp submit succesfully. UserOp hash: %v", txResult)
}
