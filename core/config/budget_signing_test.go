package config

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v2"
)

func TestBudgetSigningNetworkRollout(t *testing.T) {
	var missing *Config
	require.False(t, missing.BudgetSigningEnabledForChain(11155111))
	cfg := &Config{WalletBudgetPreparationEnabled: true, WalletBudgetSigningEnabled: true}
	require.True(t, cfg.BudgetSigningEnabledForChain(11155111))
	for _, chain := range []int64{1, 8453, 84532, 56, 0, -1} {
		require.False(t, cfg.BudgetSigningEnabledForChain(chain), "default must not broaden to %d", chain)
	}
	var raw ConfigRaw
	require.NoError(t, yaml.Unmarshal([]byte("wallet_budget_signing_chains: [1, 8453, 56]\n"), &raw))
	cfg.WalletBudgetSigningChains = raw.WalletBudgetSigningChains
	require.True(t, cfg.BudgetSigningEnabledForChain(1))
	require.True(t, cfg.BudgetSigningEnabledForChain(8453))
	require.False(t, cfg.BudgetSigningEnabledForChain(11155111), "explicit rollout replaces the default")
	require.False(t, cfg.BudgetSigningEnabledForChain(56), "configuration cannot qualify an unproved chain")
	cfg.WalletBudgetSigningEnabled = false
	require.False(t, cfg.BudgetSigningEnabledForChain(1))
	cfg.WalletBudgetSigningEnabled, cfg.WalletBudgetPreparationEnabled = true, false
	require.False(t, cfg.BudgetSigningEnabledForChain(1))
}
