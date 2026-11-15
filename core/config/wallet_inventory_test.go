package config

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestWalletInventoryQuotaConfiguration(t *testing.T) {
	t.Run("one global default", func(t *testing.T) {
		raw := ConfigRaw{}
		require.NoError(t, resolveWalletInventoryConfig(&raw))
		require.Equal(t, 3, raw.WalletInventory.MaxActiveWalletsPerOwner)
	})
	t.Run("consistent legacy values", func(t *testing.T) {
		raw := ConfigRaw{Chains: []ChainConfigRaw{{SmartWallet: SmartWalletConfigRaw{MaxWalletsPerOwner: 3}}, {SmartWallet: SmartWalletConfigRaw{MaxWalletsPerOwner: 3}}}}
		require.NoError(t, resolveWalletInventoryConfig(&raw))
		require.Equal(t, 3, raw.WalletInventory.MaxActiveWalletsPerOwner)
	})
	t.Run("conflicting legacy values", func(t *testing.T) {
		raw := ConfigRaw{Chains: []ChainConfigRaw{{SmartWallet: SmartWalletConfigRaw{MaxWalletsPerOwner: 3}}, {SmartWallet: SmartWalletConfigRaw{MaxWalletsPerOwner: 5}}}}
		require.Error(t, resolveWalletInventoryConfig(&raw))
		raw.WalletInventory = &WalletInventoryConfig{MaxActiveWalletsPerOwner: 3}
		require.NoError(t, resolveWalletInventoryConfig(&raw))
	})
	t.Run("invalid explicit limit", func(t *testing.T) {
		raw := ConfigRaw{WalletInventory: &WalletInventoryConfig{}}
		require.Error(t, resolveWalletInventoryConfig(&raw))
	})
}
