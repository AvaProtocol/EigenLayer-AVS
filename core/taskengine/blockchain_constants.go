package taskengine

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
	// 30 days: 7,200 blocks/day × 30 = 216,000 blocks
	// 60 days: 7,200 blocks/day × 60 = 432,000 blocks
	// 120 days: 7,200 blocks/day × 120 = 864,000 blocks
	1: {
		OneMonth:   216000,
		TwoMonths:  432000,
		FourMonths: 864000,
	},

	// Ethereum Sepolia Testnet (Chain ID: 11155111)
	// Block time: ~12 seconds (same as mainnet)
	// Optimized for faster responses while maintaining good coverage
	11155111: {
		OneMonth:   216000, // ~30 days of history
		TwoMonths:  432000, // ~60 days of history
		FourMonths: 864000, // ~120 days of history
	},

	// Base Mainnet (Chain ID: 8453)
	// Block time: ~2 seconds
	// 30 days: 43,200 blocks/day × 30 = 1,296,000 blocks
	// 60 days: 43,200 blocks/day × 60 = 2,592,000 blocks
	// 120 days: 43,200 blocks/day × 120 = 5,184,000 blocks
	8453: {
		OneMonth:   1296000,
		TwoMonths:  2592000,
		FourMonths: 5184000,
	},

	// Base Sepolia Testnet (Chain ID: 84532)
	// Block time: ~2 seconds (same as mainnet)
	84532: {
		OneMonth:   1296000,
		TwoMonths:  2592000,
		FourMonths: 5184000,
	},

	// BNB Smart Chain Mainnet (Chain ID: 56)
	// Block time: ~0.75 seconds (after Maxwell hardfork)
	// 30 days: 115,200 blocks/day × 30 = 3,456,000 blocks
	// 60 days: 115,200 blocks/day × 60 = 6,912,000 blocks
	// 120 days: 115,200 blocks/day × 120 = 13,824,000 blocks
	56: {
		OneMonth:   3456000,
		TwoMonths:  6912000,
		FourMonths: 13824000,
	},

	// BNB Smart Chain Testnet (Chain ID: 97)
	// Block time: ~0.75 seconds (same as mainnet)
	97: {
		OneMonth:   3456000,
		TwoMonths:  6912000,
		FourMonths: 13824000,
	},

	// Polygon Mainnet (Chain ID: 137)
	// Block time: ~2 seconds
	// 30 days: 43,200 blocks/day × 30 = 1,296,000 blocks
	// 60 days: 43,200 blocks/day × 60 = 2,592,000 blocks
	// 120 days: 43,200 blocks/day × 120 = 5,184,000 blocks
	137: {
		OneMonth:   1296000,
		TwoMonths:  2592000,
		FourMonths: 5184000,
	},

	// Polygon Mumbai Testnet (Chain ID: 80001)
	// Block time: ~2 seconds (same as mainnet)
	80001: {
		OneMonth:   1296000,
		TwoMonths:  2592000,
		FourMonths: 5184000,
	},

	// Avalanche C-Chain (Chain ID: 43114)
	// Block time: ~2 seconds
	// 30 days: 43,200 blocks/day × 30 = 1,296,000 blocks
	// 60 days: 43,200 blocks/day × 60 = 2,592,000 blocks
	// 120 days: 43,200 blocks/day × 120 = 5,184,000 blocks
	43114: {
		OneMonth:   1296000,
		TwoMonths:  2592000,
		FourMonths: 5184000,
	},

	// Avalanche Fuji Testnet (Chain ID: 43113)
	// Block time: ~2 seconds (same as mainnet)
	43113: {
		OneMonth:   1296000,
		TwoMonths:  2592000,
		FourMonths: 5184000,
	},
}

// DefaultBlockSearchRanges provides fallback values for unknown chains
// Based on a conservative 12-second block time (Ethereum-like)
var DefaultBlockSearchRanges = ChainBlockRanges{
	OneMonth:   216000, // 30 days at 12s blocks
	TwoMonths:  432000, // 60 days at 12s blocks
	FourMonths: 864000, // 120 days at 12s blocks
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
