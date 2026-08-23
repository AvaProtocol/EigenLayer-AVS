package apconfig

import (
	"math/big"

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

// Deployed APConfig addresses. The contract maps an operator's
// registered EigenLayer address to the alias key its node actually signs
// with, so both the operator (declaring the alias) and the aggregator
// (verifying signatures against it) need to agree on where it lives.
var (
	MainnetAddress = common.HexToAddress("0x9c02dfc92eea988902a98919bf4f035e4aaefced")
	TestnetAddress = common.HexToAddress("0xb8abbb082ecaae8d1cd68378cf3b060f6f0e07eb")
)

// AddressForChain returns the APConfig deployment for a chain. Every
// chain other than Ethereum mainnet uses the testnet deployment, which
// matches how operators have always resolved it.
func AddressForChain(chainID *big.Int) common.Address {
	if chainID != nil && chainID.Cmp(big.NewInt(1)) == 0 {
		return MainnetAddress
	}
	return TestnetAddress
}
