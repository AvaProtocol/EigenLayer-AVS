package config

import (
	"testing"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
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

func TestBudgetSigningConfigurationWarnings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chains []int64
		warned []int64
	}{
		{name: "default"},
		{name: "qualified", chains: []int64{11155111, 1, 8453}},
		{name: "typo and duplicate", chains: []int64{1, 8543, 1}, warned: []int64{8543, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zap.WarnLevel)
			log, err := sdklogging.NewZapLoggerByConfig(zap.NewDevelopmentConfig(), zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
			require.NoError(t, err)
			cfg := &Config{WalletBudgetPreparationEnabled: true, WalletBudgetSigningEnabled: true, WalletBudgetSigningChains: tc.chains}
			cfg.warnBudgetSigningChains(log)
			require.Equal(t, tc.chains, cfg.WalletBudgetSigningChains, "diagnostics must not rewrite rollout settings")
			require.False(t, cfg.BudgetSigningEnabledForChain(8543), "a typo must not enable signing")
			require.Len(t, logs.All(), len(tc.warned))
			for i, entry := range logs.All() {
				require.Contains(t, entry.Message, "wallet_budget_signing_chains")
				require.Equal(t, tc.warned[i], entry.ContextMap()["chain_id"])
			}
		})
	}
}
