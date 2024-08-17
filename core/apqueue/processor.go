package apqueue

import (
	"fmt"
	"os"

	"github.com/AvaProtocol/ap-avs/core/chainio/signer"
	"github.com/AvaProtocol/ap-avs/pkg/erc4337/bundler"
	"github.com/AvaProtocol/ap-avs/pkg/erc4337/preset"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

type ContractProcessor struct {
}

func (c *ContractProcessor) Perform(job *Job) error {
	conn, _ := ethclient.Dial(os.Getenv("RPC_URL"))
	// Because we used the  master key to signed, the address cannot be
	// calculate from that key
	owner := common.HexToAddress("0xe272b72E51a5bF8cB720fc6D6DF164a4D5E321C5")

	bundlerClient, e := bundler.NewBundlerClient("http://127.0.0.1:4337")
	if e != nil {
		panic(e)
	}

	calldata := common.FromHex("b61d27f600000000000000000000000069256ca54e6296e460dec7b29b7dcd97b81a3d55000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000000600000000000000000000000000000000000000000000000000000000000000044a9059cbb000000000000000000000000e0f7d11fd714674722d325cd86062a5f1882e13a0000000000000000000000000000000000000000000000001bc16d674ec8000000000000000000000000000000000000000000000000000000000000")

	txResult, err := preset.SendUserOp(conn, bundlerClient, signer.MustControllerSigner(), owner, calldata)
	if err != nil || txResult == "" {
		return fmt.Errorf("UseOp failed to send; error: %v", err)
	}

	//t.Logf("UserOp submit succesfully. UserOp hash: %v", txResult)

	return nil
}
