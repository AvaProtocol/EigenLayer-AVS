package config

import "fmt"

// resolveWalletInventoryConfig migrates a consistent legacy quota into one
// owner-wide value. Conflicting chain quotas require an explicit global value.
func resolveWalletInventoryConfig(raw *ConfigRaw) error {
	if raw.WalletInventory != nil {
		if raw.WalletInventory.MaxActiveWalletsPerOwner <= 0 || raw.WalletInventory.MaxActiveWalletsPerOwner > HardMaxWalletsPerOwner {
			return fmt.Errorf("smart_wallet_inventory.max_active_wallets_per_owner must be between 1 and %d", HardMaxWalletsPerOwner)
		}
		return nil
	}
	chosen := raw.SmartWallet.MaxWalletsPerOwner
	for _, chain := range raw.Chains {
		limit := chain.SmartWallet.MaxWalletsPerOwner
		if limit <= 0 {
			continue
		}
		if chosen > 0 && chosen != limit {
			return fmt.Errorf("conflicting legacy wallet quotas: configure smart_wallet_inventory.max_active_wallets_per_owner explicitly")
		}
		chosen = limit
	}
	if chosen <= 0 {
		chosen = 3
	}
	if chosen > HardMaxWalletsPerOwner {
		chosen = HardMaxWalletsPerOwner
	}
	raw.WalletInventory = &WalletInventoryConfig{MaxActiveWalletsPerOwner: chosen}
	return nil
}
