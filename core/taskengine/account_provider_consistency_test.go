package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/ethereum/go-ethereum/common"
)

// `aa` deliberately redeclares AccountProvider as its own string type instead
// of importing `config`, so that chainio carries no config dependency. The
// cost is that the two sets of string constants are independent literals: each
// package tests its own defaults, so renaming one side leaves BOTH packages
// green in isolation.
//
// The drift would surface only in production, and quietly — config decides
// what the gateway believes a chain is, `aa` decides what actually gets
// derived. Disagreement means wallets recorded under one provider and derived
// under another, which reads as users receiving unexpected addresses rather
// than as a constant mismatch.
//
// This lives in taskengine because it is the package that imports both.
func TestAccountProviderConstantsMatchAcrossPackages(t *testing.T) {
	pairs := []struct {
		name   string
		aa     aa.AccountProvider
		config string
	}{
		{"simple_account", aa.ProviderSimpleAccount, config.AccountProviderSimpleAccount},
		{"modular_account_v2", aa.ProviderModularAccountV2, config.AccountProviderModularAccountV2},
	}
	for _, p := range pairs {
		if string(p.aa) != p.config {
			t.Errorf("%s: aa has %q, config has %q — a rename on one side left the other behind",
				p.name, p.aa, p.config)
		}
	}
}

// Both packages must also agree on what an unset value means. If config
// defaulted to modular_account_v2 while aa defaulted to simple_account (or
// vice versa), a chain with no account_provider set would be recorded as one
// and derived as the other.
func TestAccountProviderDefaultsAgreeAcrossPackages(t *testing.T) {
	var swCfg config.SmartWalletConfig
	configDefault := swCfg.AccountProviderName()

	// aa's default is observable through the factory it selects for an empty
	// provider: SimpleAccount echoes back the supplied factory, MA v2 ignores
	// it and returns its own constant. The probe must therefore be an address
	// that is NOT the MA v2 factory, or the two branches are indistinguishable.
	probeFactory := common.HexToAddress("0x00000000000000000000000000000000deadbeef")
	if probeFactory == MAv2WalletFactory() {
		t.Fatal("probe address collides with the MA v2 factory; pick another")
	}
	got, err := aa.FactoryAddressForProvider("", probeFactory)
	if err != nil {
		t.Fatalf("aa.FactoryAddressForProvider(\"\"): %v", err)
	}

	aaDefaultIsSimple := got == probeFactory
	configDefaultIsSimple := configDefault == config.AccountProviderSimpleAccount

	if aaDefaultIsSimple != configDefaultIsSimple {
		t.Fatalf("default mismatch: config says %q, aa resolves an empty provider differently",
			configDefault)
	}
	// The EntryPoint v0.7 cutover made MA v2 the default on every chain. If
	// this flips back, an unconfigured chain silently returns to v0.6
	// derivation and every wallet address moves again.
	if configDefault != config.AccountProviderModularAccountV2 {
		t.Fatalf("default is %q, want %q", configDefault, config.AccountProviderModularAccountV2)
	}
}
