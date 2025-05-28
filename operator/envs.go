package operator

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// Default contract addresses for Ethereum mainnet (chainId = 1)
// These addresses are specific to Ethereum mainnet and should not be used on other networks
const (
	// Ethereum mainnet AVS Registry Coordinator address
	MainnetAVSRegistryCoordinatorAddress = "0x8DE3Ee0dE880161Aa0CD8Bf9F8F6a7AfEeB9A44B"
	// Ethereum mainnet Operator State Retriever address
	MainnetOperatorStateRetrieverAddress = "0xb3af70D5f72C04D1f490ff49e5aB189fA7122713"
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
		// Ethereum mainnet configuration
		o.apConfigAddr = common.HexToAddress("0x9c02dfc92eea988902a98919bf4f035e4aaefced")

		// Apply default contract addresses for mainnet if not provided
		if o.config.AVSRegistryCoordinatorAddress == "" {
			o.config.AVSRegistryCoordinatorAddress = MainnetAVSRegistryCoordinatorAddress
			o.logger.Warnf("AVS Registry Coordinator Address not provided for mainnet, using default: %s", o.config.AVSRegistryCoordinatorAddress)
		}
		if o.config.OperatorStateRetrieverAddress == "" {
			o.config.OperatorStateRetrieverAddress = MainnetOperatorStateRetrieverAddress
			o.logger.Warnf("Operator State Retriever Address not provided for mainnet, using default: %s", o.config.OperatorStateRetrieverAddress)
		}

		if o.config.TargetChain.EthRpcUrl == "" {
			o.config.TargetChain.EthRpcUrl = o.config.EthRpcUrl
			o.config.TargetChain.EthWsUrl = o.config.EthWsUrl
		}
	} else {
		// Testnet configuration
		o.apConfigAddr = common.HexToAddress("0xb8abbb082ecaae8d1cd68378cf3b060f6f0e07eb")

		// For testnets, we don't have default addresses - they must be provided in config
		if o.config.AVSRegistryCoordinatorAddress == "" {
			o.logger.Errorf("AVS Registry Coordinator Address is required for testnet (chainId: %s)", chainID.String())
			return fmt.Errorf("AVS Registry Coordinator Address is required for testnet (chainId: %s)", chainID.String())
		}
		if o.config.OperatorStateRetrieverAddress == "" {
			o.logger.Errorf("Operator State Retriever Address is required for testnet (chainId: %s)", chainID.String())
			return fmt.Errorf("Operator State Retriever Address is required for testnet (chainId: %s)", chainID.String())
		}
	}

	o.logger.Infof("Chain configuration applied for chainId: %s", chainID.String())
	return nil
}
