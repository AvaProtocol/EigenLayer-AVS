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
//
// These match the README "Contract address" tables. They are compiled
// in rather than taken from YAML so the two sides cannot drift.
var (
	MainnetAddress = common.HexToAddress("0x9c02dfc92eea988902a98919bf4f035e4aaefced")
	// Live Sepolia proxy. 0xb8abbb… is an older proxy (see
	// contracts/script/DeployAPConfig.s.sol); eth_getCode is empty
	// there and v4.17.3 refused to start the gateway because of it.
	TestnetAddress = common.HexToAddress("0xFf2E98967A8607F9Acd5CC9024cC5dE5DF0D60a3")
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
