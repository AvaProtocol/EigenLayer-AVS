package config

import (
	"slices"

	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
)

func budgetSigningChainQualified(chain int64) bool {
	switch chain {
	case 1, 8453, 11155111: // Ethereum, Base, Sepolia; qualified protocol deployments
		return true
	default:
		return false
	}
}

// BudgetSigningEnabledForChain keeps deployment opt-in separate from protocol
// support. Merely upgrading the binary must not enable mainnet signing.
func (c *Config) BudgetSigningEnabledForChain(chain int64) bool {
	if c == nil || !c.WalletBudgetPreparationEnabled || !c.WalletBudgetSigningEnabled {
		return false
	}
	if !budgetSigningChainQualified(chain) {
		return false
	}
	if len(c.WalletBudgetSigningChains) == 0 {
		return chain == 11155111
	}
	return slices.Contains(c.WalletBudgetSigningChains, chain)
}

// Report ignored entries without changing rollout settings or broadening support.
func (c *Config) warnBudgetSigningChains(log logger.Logger) {
	seen := make(map[int64]bool, len(c.WalletBudgetSigningChains))
	for _, chain := range c.WalletBudgetSigningChains {
		if seen[chain] {
			log.Warn("Ignoring duplicate wallet_budget_signing_chains entry", "chain_id", chain)
			continue
		}
		seen[chain] = true
		if !budgetSigningChainQualified(chain) {
			log.Warn("Ignoring unqualified wallet_budget_signing_chains entry", "chain_id", chain)
		}
	}
}
