package config

import "testing"

// Mirrors the aa-package default test at the config layer, because the two
// defaults have to agree: config decides what the gateway believes, aa decides
// what gets derived. If they ever diverge, wallets get recorded under one
// provider and derived under the other.
func TestAccountProviderDefaultsToSimpleAccount(t *testing.T) {
	var c SmartWalletConfig
	if got := c.AccountProviderName(); got != AccountProviderSimpleAccount {
		t.Fatalf("default = %q, want %q — defaulting to MA v2 would move every existing wallet",
			got, AccountProviderSimpleAccount)
	}
	if c.UsesModularAccountV2() {
		t.Error("UsesModularAccountV2 is true by default")
	}
}

func TestAccountProviderNormalisation(t *testing.T) {
	for in, want := range map[string]string{
		"":                      AccountProviderSimpleAccount,
		"  ":                    AccountProviderSimpleAccount,
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
// during config load: a typo like "mav2" was accepted, AccountProviderName()
// returned it verbatim, and UsesModularAccountV2() went false — so a chain the
// operator believed was on MA v2 silently handed users v0.6 addresses. The
// function existing is not the same as it running.
func TestTypoWouldSilentlyReadAsSimpleAccount(t *testing.T) {
	c := SmartWalletConfig{AccountProvider: "mav2"}

	if c.UsesModularAccountV2() {
		t.Fatal("precondition changed")
	}
	// This is the trap: it looks harmless. Only validation catches it.
	if err := c.ValidateAccountProvider(); err == nil {
		t.Fatal("a typo must be rejected; otherwise it degrades silently to simple_account")
	}
}
