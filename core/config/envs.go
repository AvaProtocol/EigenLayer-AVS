package config

import "math/big"

type ChainEnv string

const (
	SepoliaEnv     = ChainEnv("sepolia")
	EthereumEnv    = ChainEnv("ethereum")
	BaseEnv        = ChainEnv("base")
	BaseSepoliaEnv = ChainEnv("base-sepolia")
)

var (
	MainnetChainID     = big.NewInt(1)
	SepoliaChainID     = big.NewInt(11155111)
	BaseChainID        = big.NewInt(8453)
	BaseSepoliaChainID = big.NewInt(84532)
	CurrentChainEnv    = ChainEnv("ethereum")

	etherscanURLs = map[ChainEnv]string{
		EthereumEnv:    "https://etherscan.io",
		SepoliaEnv:     "https://sepolia.etherscan.io",
		BaseEnv:        "https://basescan.org",
		BaseSepoliaEnv: "https://sepolia.basescan.org",
	}

	eigenlayerAppURLs = map[ChainEnv]string{
		EthereumEnv: "https://app.eigenlayer.xyz",
		SepoliaEnv:  "https://sepolia.eigenlayer.xyz",
	}
)

// ChainEnvForChainID returns the ChainEnv for a given chain ID.
// Falls back to CurrentChainEnv if the chain ID is unknown.
func ChainEnvForChainID(chainID int64) ChainEnv {
	switch chainID {
	case 1:
		return EthereumEnv
	case 11155111:
		return SepoliaEnv
	case 8453:
		return BaseEnv
	case 84532:
		return BaseSepoliaEnv
	default:
		return CurrentChainEnv
	}
}

// EtherscanURLForChainID returns the block explorer URL for a given chain ID.
func EtherscanURLForChainID(chainID int64) string {
	env := ChainEnvForChainID(chainID)
	if url, ok := etherscanURLs[env]; ok {
		return url
	}
	return "https://etherscan.io"
}

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
