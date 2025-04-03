package config

import (
	"github.com/ethereum/go-ethereum/common"
)

func convertToAddressSlice(addresses []string) []common.Address {
	result := make([]common.Address, len(addresses))
	for i, addr := range addresses {
		result[i] = common.HexToAddress(addr)
	}
	return result
}
