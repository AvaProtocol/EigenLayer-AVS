package preset

import (
	"crypto/ecdsa"
	"math/big"
	"os"
	"testing"

	"github.com/AvaProtocol/ap-avs/core/chainio/aa"
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
	conn, _ := ethclient.Dial(os.Getenv("RPC_URL"))
	// Because we used the  master key to signed, the address cannot be
	// calculate from that key
	owner := common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5")
	bundlerClient, e := bundler.NewBundlerClient(os.Getenv("BUNDLER_RPC"))
	if e != nil {
		panic(e)
	}

	//calldata := common.FromHex("b61d27f600000000000000000000000069256ca54e6296e460dec7b29b7dcd97b81a3d55000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000600000000000000000000000000000000000000000000000000000000000000044a9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a0000000000000000000000000000000000000000000000001bc16d674ec8000000000000000000000000000000000000000000000000000000000000")
	calldata, e := aa.PackExecute(
		// Test token on sepolia
		common.HexToAddress("0x69256ca54e6296e460dec7b29b7dcd97b81a3d55"),
		big.NewInt(0),
		// Transfe 0.00761 the test token
		// https://sepolia.etherscan.io/tx/0xb438f9583a0a505806fa756e1950430e6060c8b4662807c6c40b9f2dec9d726f
		common.FromHex("0xa9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a000000000000000000000000000000000000000000000000001b094132bda000"),
	)

	txResult, err := SendUserOp(conn, bundlerClient, getControllerSigner(), owner, calldata)
	if err != nil || txResult == "" {
		t.Errorf("UseOp failed to send; error:%v", err)
	}

	t.Logf("UserOp submit succesfully. UserOp hash: %v", txResult)
}
