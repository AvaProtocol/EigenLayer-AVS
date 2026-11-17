package config

import "slices"

// BudgetSigningEnabledForChain keeps deployment opt-in separate from protocol
// support. Merely upgrading the binary must not enable mainnet signing.
func (c *Config) BudgetSigningEnabledForChain(chain int64) bool {
	if c == nil || !c.WalletBudgetPreparationEnabled || !c.WalletBudgetSigningEnabled {
		return false
	}
	switch chain {
	case 1, 8453, 11155111: // Ethereum, Base, Sepolia; qualified protocol deployments
	default:
		return false
	}
	if len(c.WalletBudgetSigningChains) == 0 {
		return chain == 11155111
	}
	return slices.Contains(c.WalletBudgetSigningChains, chain)
}
