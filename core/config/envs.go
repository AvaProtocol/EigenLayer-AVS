package config

import "math/big"

type ChainEnv string

const (
	SepoliaEnv  = ChainEnv("sepolia")
	EthereumEnv = ChainEnv("ethereum")
)

var (
	MainnetChainID  = big.NewInt(1)
	CurrentChainEnv = ChainEnv("ethereum")

	etherscanURLs = map[ChainEnv]string{
		EthereumEnv: "https://etherscan.io",
		SepoliaEnv:  "https://sepolia.etherscan.io",
	}

	eigenlayerAppURLs = map[ChainEnv]string{
		EthereumEnv: "https://app.eigenlayer.xyz",
		SepoliaEnv:  "https://sepolia.eigenlayer.xyz",
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
