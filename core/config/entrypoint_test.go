package config

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// EntrypointAddress (the YAML field) is the v0.6 EntryPoint. Using it on an
// MA v2 chain does not fail loudly — it produces userOp hashes that match
// nothing on chain, and receipt polling that waits out its full timeout
// watching a contract the operation never touched.
func TestEntryPointAddressFollowsAccountProvider(t *testing.T) {
	configured := common.HexToAddress(DefaultEntrypointAddressHex)
	v07 := common.HexToAddress(EntryPointV07AddressHex)

	if configured == v07 {
		t.Fatal("the v0.6 and v0.7 EntryPoints must be different addresses")
	}

	t.Run("MA v2 uses the canonical v0.7 EntryPoint", func(t *testing.T) {
		c := SmartWalletConfig{
			EntrypointAddress: configured,
			AccountProvider:   AccountProviderModularAccountV2,
		}
		if got := c.EntryPointAddress(); got != v07 {
			t.Errorf("EntryPointAddress = %s, want %s", got.Hex(), v07.Hex())
		}
	})

	t.Run("unset provider is MA v2, so also v0.7", func(t *testing.T) {
		c := SmartWalletConfig{EntrypointAddress: configured}
		if got := c.EntryPointAddress(); got != v07 {
			t.Errorf("EntryPointAddress = %s, want %s", got.Hex(), v07.Hex())
		}
	})

	t.Run("simple_account keeps the configured address", func(t *testing.T) {
		c := SmartWalletConfig{
			EntrypointAddress: configured,
			AccountProvider:   AccountProviderSimpleAccount,
		}
		if got := c.EntryPointAddress(); got != configured {
			t.Errorf("EntryPointAddress = %s, want %s", got.Hex(), configured.Hex())
		}
	})

	// A YAML override must still reach the v0.6 path — it is the only
	// EntryPoint that is per-deployment.
	t.Run("simple_account honours a YAML override", func(t *testing.T) {
		custom := common.HexToAddress("0x00000000000000000000000000000000000000ee")
		c := SmartWalletConfig{
			EntrypointAddress: custom,
			AccountProvider:   AccountProviderSimpleAccount,
		}
		if got := c.EntryPointAddress(); got != custom {
			t.Errorf("EntryPointAddress = %s, want %s", got.Hex(), custom.Hex())
		}
	})
}
