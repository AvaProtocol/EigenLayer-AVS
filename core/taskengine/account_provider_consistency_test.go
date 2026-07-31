package taskengine

import (
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
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
// defaulted to simple_account while aa defaulted to modular_account_v2 (or
// vice versa), a chain with no account_provider set would be recorded as one
// and derived as the other.
func TestAccountProviderDefaultsAgreeAcrossPackages(t *testing.T) {
	var swCfg config.SmartWalletConfig
	configDefault := swCfg.AccountProviderName()

	// aa's default is observable through the factory it selects for an empty
	// provider: SimpleAccount uses the supplied factory, MA v2 ignores it.
	simpleFactory := MAv2WalletFactory() // any non-zero address works as a probe
	got, err := aa.FactoryAddressForProvider("", simpleFactory)
	if err != nil {
		t.Fatalf("aa.FactoryAddressForProvider(\"\"): %v", err)
	}

	aaDefaultIsSimple := got == simpleFactory
	configDefaultIsSimple := configDefault == config.AccountProviderSimpleAccount

	if aaDefaultIsSimple != configDefaultIsSimple {
		t.Fatalf("default mismatch: config says %q, aa resolves an empty provider differently",
			configDefault)
	}
	if !configDefaultIsSimple {
		t.Fatal("default is no longer simple_account; enabling MA v2 by default moves every existing wallet")
	}
}
