package config

import "math/big"

type ChainEnv string

const (
	HoleskyEnv  = ChainEnv("holesky")
	EthereumEnv = ChainEnv("ethereum")
)

var (
	MainnetChainID  = big.NewInt(1)
	CurrentChainEnv = ChainEnv("ethereum")
)

func IsMainnet() bool {
	return CurrentChainEnv == HoleskyEnv
}

func EtherscanURL() string {
	if IsMainnet() {
		return "https://etherscan.io"
	}
	return "https://holesky.etherscan.io"
}

func EigenlayerAppURL() string {
	if IsMainnet() {
		return "https://app.eigenlayer.xyz"
	}
	return "https://holesky.eigenlayer.xyz"
}
