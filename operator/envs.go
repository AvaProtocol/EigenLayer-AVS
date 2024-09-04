package operator

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// Populate configuration based on known env
// TODO: We can fetch this dynamically from aggregator so we can upgrade the
// config without the need to release operator
var (
	mainnetChainID = big.NewInt(1)
)

// Populate config based on chain id.
// There is a certain config that is depend based on env
func (o *Operator) PopulateKnownConfigByChainID(chainID *big.Int) error {
	if chainID.Cmp(mainnetChainID) == 0 {
		o.apConfigAddr = common.HexToAddress("0x9c02dfc92eea988902a98919bf4f035e4aaefced")

		if o.config.TargetChain.EthRpcUrl == "" {
			o.config.TargetChain.EthRpcUrl = o.config.EthRpcUrl
			o.config.TargetChain.EthWsUrl = o.config.EthWsUrl
		}
	} else {
		// Testnet
		o.apConfigAddr = common.HexToAddress("0xb8abbb082ecaae8d1cd68378cf3b060f6f0e07eb")
	}

	return nil
}
