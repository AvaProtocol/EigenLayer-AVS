package taskengine

import "github.com/ethereum/go-ethereum/common"

// Mainnet entries are exercised by TestBudgetAtomicResetAndInstall_MainnetForks
// with the exact production reset/install encoders, real deployed tokens and
// stock MA v2 modules. Sepolia USDC additionally has the live bundler proof.
// Unknown/nonqualified nonzero approvals still require owner-directed revoke.
var budgetAutomaticResetTokens = map[int64][]common.Address{
	11155111: {common.HexToAddress("0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238")},
	1: {
		common.HexToAddress("0xA0b86991c6218b36c1d19D4a2e9Eb0cE3606eB48"), // USDC
		common.HexToAddress("0xdAC17F958D2ee523a2206206994597C13D831ec7"), // USDT
		common.HexToAddress("0xC02aaA39b223FE8D0A0e5C4F27eAD9083C756Cc2"), // WETH
		common.HexToAddress("0x54D2252757e1672EEaD234D27B1270728fF90581"), // canonical BGB
	},
	8453: {
		common.HexToAddress("0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913"), // native USDC
		common.HexToAddress("0x4200000000000000000000000000000000000006"), // WETH
	},
}

func budgetResetTokenTested(chainID int64, token common.Address) bool {
	for _, tested := range budgetAutomaticResetTokens[chainID] {
		if token == tested {
			return true
		}
	}
	return false
}
