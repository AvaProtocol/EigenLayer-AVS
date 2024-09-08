package aa

import (
	"github.com/ethereum/go-ethereum/common"
)

var (
	EntrypointAddress = common.HexToAddress("0x5FF137D4b0FDCD49DcA30c7CF57E578a026d2789")
	factoryAddress    = common.HexToAddress("0x29adA1b5217242DEaBB142BC3b1bCfFdd56008e7")
)

func SetFactoryAddress(address common.Address) {
	factoryAddress = address
}

func SetEntrypointAddress(address common.Address) {
	EntrypointAddress = address
}
