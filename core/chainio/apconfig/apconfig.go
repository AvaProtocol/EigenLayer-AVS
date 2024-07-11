package apconfig

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

func GetContract(conn *ethclient.Client, address common.Address) (*APConfig, error) {
	return NewAPConfig(address, conn)
}
