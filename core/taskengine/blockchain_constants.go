package taskengine

// ChainBlockRanges defines the search ranges for different blockchain networks
// Based on chain-specific block times and configured for 3-month and 6-month periods
type ChainBlockRanges struct {
	ThreeMonths uint64 // ~90 days worth of blocks
	SixMonths   uint64 // ~180 days worth of blocks
}

// BlockSearchRanges maps chain IDs to their respective block search ranges
// These are calculated based on average block times and target time periods
var BlockSearchRanges = map[uint64]ChainBlockRanges{
	// Ethereum Mainnet (Chain ID: 1)
	// Block time: ~12 seconds
	// 90 days: 7,200 blocks/day × 90 = 648,000 blocks
	// 180 days: 7,200 blocks/day × 180 = 1,296,000 blocks
	1: {
		ThreeMonths: 648000,
		SixMonths:   1296000,
	},

	// Ethereum Sepolia Testnet (Chain ID: 11155111)
	// Block time: ~12 seconds (same as mainnet)
	11155111: {
		ThreeMonths: 648000,
		SixMonths:   1296000,
	},

	// Base Mainnet (Chain ID: 8453)
	// Block time: ~2 seconds
	// 90 days: 43,200 blocks/day × 90 = 3,888,000 blocks
	// 180 days: 43,200 blocks/day × 180 = 7,776,000 blocks
	8453: {
		ThreeMonths: 3888000,
		SixMonths:   7776000,
	},

	// Base Sepolia Testnet (Chain ID: 84532)
	// Block time: ~2 seconds (same as mainnet)
	84532: {
		ThreeMonths: 3888000,
		SixMonths:   7776000,
	},

	// BNB Smart Chain Mainnet (Chain ID: 56)
	// Block time: ~0.75 seconds (after Maxwell hardfork)
	// 90 days: 115,200 blocks/day × 90 = 10,368,000 blocks
	// 180 days: 115,200 blocks/day × 180 = 20,736,000 blocks
	56: {
		ThreeMonths: 10368000,
		SixMonths:   20736000,
	},

	// BNB Smart Chain Testnet (Chain ID: 97)
	// Block time: ~0.75 seconds (same as mainnet)
	97: {
		ThreeMonths: 10368000,
		SixMonths:   20736000,
	},

	// Polygon Mainnet (Chain ID: 137)
	// Block time: ~2 seconds
	// 90 days: 43,200 blocks/day × 90 = 3,888,000 blocks
	// 180 days: 43,200 blocks/day × 180 = 7,776,000 blocks
	137: {
		ThreeMonths: 3888000,
		SixMonths:   7776000,
	},

	// Polygon Mumbai Testnet (Chain ID: 80001)
	// Block time: ~2 seconds (same as mainnet)
	80001: {
		ThreeMonths: 3888000,
		SixMonths:   7776000,
	},

	// Avalanche C-Chain (Chain ID: 43114)
	// Block time: ~2 seconds
	// 90 days: 43,200 blocks/day × 90 = 3,888,000 blocks
	// 180 days: 43,200 blocks/day × 180 = 7,776,000 blocks
	43114: {
		ThreeMonths: 3888000,
		SixMonths:   7776000,
	},

	// Avalanche Fuji Testnet (Chain ID: 43113)
	// Block time: ~2 seconds (same as mainnet)
	43113: {
		ThreeMonths: 3888000,
		SixMonths:   7776000,
	},
}

// DefaultBlockSearchRanges provides fallback values for unknown chains
// Based on a conservative 12-second block time (Ethereum-like)
var DefaultBlockSearchRanges = ChainBlockRanges{
	ThreeMonths: 648000,  // 90 days at 12s blocks
	SixMonths:   1296000, // 180 days at 12s blocks
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
// Returns only 2 ranges: 3 months and 6 months
func GetChainSearchRanges(chainID uint64) []uint64 {
	ranges := GetBlockSearchRanges(chainID)
	return []uint64{ranges.ThreeMonths, ranges.SixMonths}
}
