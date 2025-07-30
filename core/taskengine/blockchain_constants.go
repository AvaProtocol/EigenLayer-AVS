package taskengine

// Ethereum gas cost constants
const (
	// StandardGasCost represents the standard gas cost for a simple Ethereum transaction (21000 gas)
	// This is the minimum gas required for a basic ETH transfer between externally owned accounts
	StandardGasCost = uint64(21000)

	// StandardGasCostHex is the hexadecimal representation of StandardGasCost
	// Used in transaction receipts and other hex-encoded contexts
	StandardGasCostHex = "0x5208"
)

// Contract method constants
const (
	// UnknownMethodName represents a placeholder for contract method names that need to be resolved from ABI
	// Used when call_data is available but method_name is not explicitly provided
	UnknownMethodName = "unknown"
)

// ChainBlockRanges defines the search ranges for different blockchain networks
// Based on chain-specific block times and configured for 1-month, 2-month, and 4-month periods
type ChainBlockRanges struct {
	OneMonth   uint64 // ~30 days worth of blocks
	TwoMonths  uint64 // ~60 days worth of blocks
	FourMonths uint64 // ~120 days worth of blocks
}

// BlockSearchRanges maps chain IDs to their respective block search ranges
// These are calculated based on average block times and target time periods
var BlockSearchRanges = map[uint64]ChainBlockRanges{
	// Ethereum Mainnet (Chain ID: 1)
	// Block time: ~12 seconds
	// 1/10 of original ranges to balance search coverage with RPC limits
	1: {
		OneMonth:   21600, // ~3 days of recent history (1/10 of 216000)
		TwoMonths:  43200, // ~6 days of recent history (1/10 of 432000)
		FourMonths: 86400, // ~12 days of recent history (1/10 of 864000)
	},

	// Ethereum Sepolia Testnet (Chain ID: 11155111)
	// Block time: ~12 seconds (same as mainnet)
	// 1/10 of original ranges to balance search coverage with RPC limits
	11155111: {
		OneMonth:   21600, // ~3 days of recent history (1/10 of 216000)
		TwoMonths:  43200, // ~6 days of recent history (1/10 of 432000)
		FourMonths: 86400, // ~12 days of recent history (1/10 of 864000)
	},

	// Base Mainnet (Chain ID: 8453)
	// Block time: ~2 seconds
	// 1/10 of original ranges to balance search coverage with RPC limits
	8453: {
		OneMonth:   129600, // ~3 days of recent history (1/10 of 1296000)
		TwoMonths:  259200, // ~6 days of recent history (1/10 of 2592000)
		FourMonths: 518400, // ~12 days of recent history (1/10 of 5184000)
	},

	// Base Sepolia Testnet (Chain ID: 84532)
	// Block time: ~2 seconds (same as mainnet)
	84532: {
		OneMonth:   129600, // ~3 days of recent history (1/10 of 1296000)
		TwoMonths:  259200, // ~6 days of recent history (1/10 of 2592000)
		FourMonths: 518400, // ~12 days of recent history (1/10 of 5184000)
	},

	// BNB Smart Chain Mainnet (Chain ID: 56)
	// Block time: ~0.75 seconds (after Maxwell hardfork)
	// 1/10 of original ranges to balance search coverage with RPC limits
	56: {
		OneMonth:   345600,  // ~3 days of recent history (1/10 of 3456000)
		TwoMonths:  691200,  // ~6 days of recent history (1/10 of 6912000)
		FourMonths: 1382400, // ~12 days of recent history (1/10 of 13824000)
	},

	// BNB Smart Chain Testnet (Chain ID: 97)
	// Block time: ~0.75 seconds (same as mainnet)
	97: {
		OneMonth:   345600,  // ~3 days of recent history (1/10 of 3456000)
		TwoMonths:  691200,  // ~6 days of recent history (1/10 of 6912000)
		FourMonths: 1382400, // ~12 days of recent history (1/10 of 13824000)
	},

	// Polygon Mainnet (Chain ID: 137)
	// Block time: ~2 seconds
	// 1/10 of original ranges to balance search coverage with RPC limits
	137: {
		OneMonth:   129600, // ~3 days of recent history (1/10 of 1296000)
		TwoMonths:  259200, // ~6 days of recent history (1/10 of 2592000)
		FourMonths: 518400, // ~12 days of recent history (1/10 of 5184000)
	},

	// Polygon Mumbai Testnet (Chain ID: 80001)
	// Block time: ~2 seconds (same as mainnet)
	80001: {
		OneMonth:   129600, // ~3 days of recent history (1/10 of 1296000)
		TwoMonths:  259200, // ~6 days of recent history (1/10 of 2592000)
		FourMonths: 518400, // ~12 days of recent history (1/10 of 5184000)
	},

	// Avalanche C-Chain (Chain ID: 43114)
	// Block time: ~2 seconds
	// 1/10 of original ranges to balance search coverage with RPC limits
	43114: {
		OneMonth:   129600, // ~3 days of recent history (1/10 of 1296000)
		TwoMonths:  259200, // ~6 days of recent history (1/10 of 2592000)
		FourMonths: 518400, // ~12 days of recent history (1/10 of 5184000)
	},

	// Avalanche Fuji Testnet (Chain ID: 43113)
	// Block time: ~2 seconds (same as mainnet)
	43113: {
		OneMonth:   129600, // ~3 days of recent history (1/10 of 1296000)
		TwoMonths:  259200, // ~6 days of recent history (1/10 of 2592000)
		FourMonths: 518400, // ~12 days of recent history (1/10 of 5184000)
	},
}

// DefaultBlockSearchRanges provides fallback values for unknown chains
// 1/10 of original conservative ranges to balance search coverage with RPC limits
var DefaultBlockSearchRanges = ChainBlockRanges{
	OneMonth:   21600, // ~3 days at 12s blocks (1/10 of 216000)
	TwoMonths:  43200, // ~6 days at 12s blocks (1/10 of 432000)
	FourMonths: 86400, // ~12 days at 12s blocks (1/10 of 864000)
}

// GetBlockSearchRanges returns the appropriate search ranges for a given chain ID
// Falls back to DefaultBlockSearchRanges for unknown chains
func GetBlockSearchRanges(chainID uint64) ChainBlockRanges {
	if ranges, exists := BlockSearchRanges[chainID]; exists {
		return ranges
	}
	return DefaultBlockSearchRanges
}

// GetChainSearchRanges returns the search ranges as a slice for use in search loops
// Returns 3 ranges: 1 month, 2 months, and 4 months
func GetChainSearchRanges(chainID uint64) []uint64 {
	ranges := GetBlockSearchRanges(chainID)
	return []uint64{ranges.OneMonth, ranges.TwoMonths, ranges.FourMonths}
}
