package apconfig

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

func GetContract(ethRpcURL string, address common.Address) (*APConfig, error) {
	ethRpcClient, err := ethclient.Dial(ethRpcURL)
	if err != nil {
		return nil, err
	}

	return NewAPConfig(address, ethRpcClient)
}
