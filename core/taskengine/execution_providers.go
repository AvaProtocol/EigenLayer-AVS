package taskengine

// ExecutionProvider represents the type of execution environment for contract interactions
type ExecutionProvider string

const (
	// ProviderChainRPC represents direct RPC calls to blockchain nodes
	ProviderChainRPC ExecutionProvider = "chain_rpc"

	// ProviderTenderly represents simulated calls using Tenderly's simulation service
	ProviderTenderly ExecutionProvider = "tenderly"

	// ProviderBundler represents calls executed through ERC4337 bundlers
	ProviderBundler ExecutionProvider = "bundler"
)

// Use existing chain ID constants from token_metadata.go

// GetNetworkName returns a human-readable name for the given chain ID
func GetNetworkName(chainID int64) string {
	switch uint64(chainID) {
	case ChainIDEthereum:
		return "ethereum"
	case ChainIDSepolia:
		return "sepolia"
	case ChainIDBase:
		return "base"
	case ChainIDBaseSepolia:
		return "base-sepolia"
	default:
		return "unknown"
	}
}

// GetProviderForContext returns the appropriate provider based on execution context
func GetProviderForContext(isSimulated bool, chainID int64) ExecutionProvider {
	if isSimulated {
		return ProviderTenderly
	}
	return ProviderChainRPC
}

// GetExecutionContext creates a standardized execution context map
func GetExecutionContext(chainID int64, isSimulated bool) map[string]interface{} {
	provider := GetProviderForContext(isSimulated, chainID)

	return map[string]interface{}{
		"chainId":     chainID,
		"isSimulated": isSimulated,
		"provider":    string(provider),
	}
}
