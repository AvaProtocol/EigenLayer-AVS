package config

import "testing"

// Mirrors the aa-package default test at the config layer, because the two
// defaults have to agree: config decides what the gateway believes, aa decides
// what gets derived. If they ever diverge, wallets get recorded under one
// provider and derived under the other.
func TestAccountProviderDefaultsToModularAccountV2(t *testing.T) {
	var c SmartWalletConfig
	if got := c.AccountProviderName(); got != AccountProviderModularAccountV2 {
		t.Fatalf("default = %q, want %q — the v0.7 cutover made MA v2 the default on every chain",
			got, AccountProviderModularAccountV2)
	}
	if !c.UsesModularAccountV2() {
		t.Error("UsesModularAccountV2 is false by default")
	}
}

func TestAccountProviderNormalisation(t *testing.T) {
	for in, want := range map[string]string{
		"":                      AccountProviderModularAccountV2,
		"  ":                    AccountProviderModularAccountV2,
		"simple_account":        AccountProviderSimpleAccount,
		"MODULAR_ACCOUNT_V2":    AccountProviderModularAccountV2,
		" modular_account_v2  ": AccountProviderModularAccountV2,
	} {
		c := SmartWalletConfig{AccountProvider: in}
		if got := c.AccountProviderName(); got != want {
			t.Errorf("AccountProviderName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A typo must fail loudly. Silently falling back would derive v0.6 addresses
// on a chain the operator believed was on MA v2, visible only as users getting
// unexpected addresses.
func TestValidateAccountProvider(t *testing.T) {
	for _, ok := range []string{"", "simple_account", "modular_account_v2", "Modular_Account_V2"} {
		c := SmartWalletConfig{AccountProvider: ok}
		if err := c.ValidateAccountProvider(); err != nil {
			t.Errorf("ValidateAccountProvider(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"modular_account", "mav2", "simple", "v0.7"} {
		c := SmartWalletConfig{AccountProvider: bad}
		if err := c.ValidateAccountProvider(); err == nil {
			t.Errorf("ValidateAccountProvider(%q) = nil, want an error", bad)
		}
	}
}

// Before this was wired, ValidateAccountProvider existed but was never called
// during config load. A typo like "mav2" was accepted and returned verbatim by
// AccountProviderName(), so UsesModularAccountV2() went false on a chain the
// operator believed was on MA v2. The function existing is not the same as it
// running.
//
// Since the default flipped, a typo no longer degrades to v0.6 addresses — it
// reaches aa.DeriveSenderAddress as an unknown provider and errors there. That
// is a louder failure but a much later one, at the first wallet derivation
// rather than at boot, which is why validation at load still matters.
func TestTypoIsRejectedAtLoad(t *testing.T) {
	c := SmartWalletConfig{AccountProvider: "mav2"}

	if c.UsesModularAccountV2() {
		t.Fatal("precondition changed")
	}
	// This is the trap: it looks harmless. Only validation catches it early.
	if err := c.ValidateAccountProvider(); err == nil {
		t.Fatal("a typo must be rejected at config load, not at first derivation")
	}
}
