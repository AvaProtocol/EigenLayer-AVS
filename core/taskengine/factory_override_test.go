package taskengine

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
)

// resolveFactoryOverride is the one place the "explicit factory wins" rule
// lives for GetWallet/SetWallet. The bug it fixes: the override used to be
// applied AFTER EffectiveFactory, so a chain that could not resolve a default
// factory returned 500 even when the caller supplied a working override.

func TestResolveFactoryOverride_OverrideWinsEvenWhenDefaultUnresolvable(t *testing.T) {
	// simple_account with no configured factory: EffectiveFactory fails here.
	cfg := &config.SmartWalletConfig{
		AccountProvider: config.AccountProviderSimpleAccount,
		FactoryAddress:  common.Address{}, // unset — EffectiveFactory would error
	}
	require.Error(t, mustFactoryErr(cfg), "precondition: this config must fail EffectiveFactory")

	override := "0x00000000000017c61b5bEe81050EC8eFc9c6fecd"
	got, err := resolveFactoryOverride(cfg, override)
	require.NoError(t, err, "a supplied override must succeed even when the chain default cannot resolve")
	require.Equal(t, common.HexToAddress(override), got)
}

func TestResolveFactoryOverride_FallsBackToChainDefault(t *testing.T) {
	// No override, MA v2 chain: resolves to the MA v2 factory.
	cfg := &config.SmartWalletConfig{AccountProvider: config.AccountProviderModularAccountV2}
	got, err := resolveFactoryOverride(cfg, "")
	require.NoError(t, err)
	require.Equal(t, aa.MAv2FactoryAddress(), got)
}

func TestResolveFactoryOverride_NoOverrideAndUnresolvableDefaultErrors(t *testing.T) {
	// No override AND an unresolvable default is the one case that must error.
	cfg := &config.SmartWalletConfig{
		AccountProvider: config.AccountProviderSimpleAccount,
		FactoryAddress:  common.Address{},
	}
	_, err := resolveFactoryOverride(cfg, "")
	require.Error(t, err)
}

func mustFactoryErr(cfg *config.SmartWalletConfig) error {
	_, err := aa.EffectiveFactory(cfg)
	return err
}
