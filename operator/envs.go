package operator

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// Populate configuration based on known env
// TODO: We can fetch this dynamically from aggregator so we can upgrade the
// config without the need to release operator
var (
	mainnetChainID = big.NewInt(0)
)

func (o *Operator) PopulateKnownConfigByChainID(chainID *big.Int) error {
	if chainID.Cmp(mainnetChainID) == 0 {
		// TODO: fill in with deployment later on
		o.apConfigAddr = common.HexToAddress("")
	} else {
		// Testnet
		o.apConfigAddr = common.HexToAddress("0xb8abbb082ecaae8d1cd68378cf3b060f6f0e07eb")
	}

	return nil
}
