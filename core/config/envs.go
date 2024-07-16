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

	etherscanURLs = map[ChainEnv]string{
		EthereumEnv: "https://etherscan.io",
		HoleskyEnv:  "https://holesky.etherscan.io",
	}

	eigenlayerAppURLs = map[ChainEnv]string{
		EthereumEnv: "https://app.eigenlayer.xyz",
		HoleskyEnv:  "https://holesky.eigenlayer.xyz",
	}
)

func EtherscanURL() string {
	if url, ok := etherscanURLs[CurrentChainEnv]; ok {
		return url
	}

	return "https://etherscan.io"
}

func EigenlayerAppURL() string {
	if url, ok := eigenlayerAppURLs[CurrentChainEnv]; ok {
		return url
	}

	return "https://eigenlayer.xyz"
}
