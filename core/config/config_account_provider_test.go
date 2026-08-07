package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

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
	// Empty takes the modular_account_v2 default; casing is normalized.
	for _, ok := range []string{"", "modular_account_v2", "Modular_Account_V2"} {
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

// simple_account is refused at load rather than accepted and then failed at
// send time. Its derivation still exists — stored wallet records reference the
// v0.6 factory — but the path that executes against those wallets was removed
// with the EntryPoint v0.7 cutover, so a chain pinned to it would hand users
// legacy addresses and fail every operation. Boot names the config line; a
// send-time failure names nothing.
func TestSimpleAccountIsRefusedAtLoad(t *testing.T) {
	c := SmartWalletConfig{AccountProvider: AccountProviderSimpleAccount, ChainID: 11155111}
	err := c.ValidateAccountProvider()
	if err == nil {
		t.Fatal("simple_account must be refused: its send path no longer exists")
	}
	if !strings.Contains(err.Error(), AccountProviderModularAccountV2) {
		t.Errorf("the refusal must name the supported value, got: %v", err)
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

// The gateway resolves the same way the worker does, through the same
// function. Pinned here as well as in the worker package because the two
// disagreeing about where sponsorship comes from is the bug this path exists
// to prevent.
func TestSponsorshipResolutionIsShared(t *testing.T) {
	t.Setenv("ALCHEMY_PAYMASTER_POLICY_ID", "")
	t.Setenv("ALCHEMY_GAS_POLICY_ID", "")

	require.Equal(t, "policy", ResolveAlchemyPaymasterPolicyID("policy", ""))
	require.Equal(t, "legacy", ResolveAlchemyPaymasterPolicyID("", "legacy"),
		"the legacy yaml alias must keep working")

	t.Setenv("ALCHEMY_PAYMASTER_POLICY_ID", "from-env")
	require.Equal(t, "from-env", ResolveAlchemyPaymasterPolicyID("", ""))

	// A blank-but-present yaml value must not swallow a good fallback: trimming
	// the winner instead of each candidate would select " " and resolve to "",
	// silently unsponsored.
	require.Equal(t, "from-env", ResolveAlchemyPaymasterPolicyID("   ", ""),
		"a whitespace-only yaml value must fall through to the environment")
	t.Setenv("GAS_MANAGER_WEBHOOK_SECRET", "secret-from-env")
	require.Equal(t, "secret-from-env", ResolveGasManagerWebhookSecret("  "),
		"same rule for the webhook secret")
}

// SponsorshipPolicyID is the single place that decides whether an operation can
// be sponsored, so every disqualifying condition has to be visible here.
func TestSponsorshipPolicyIDAppliesEveryCondition(t *testing.T) {
	alchemy := func() *SmartWalletConfig {
		return &SmartWalletConfig{
			ChainID:                  11155111,
			BundlerProvider:          BundlerProviderAlchemy,
			AlchemyAPIKey:            "key",
			AlchemyPaymasterPolicyID: "policy",
		}
	}

	require.Equal(t, "policy", alchemy().SponsorshipPolicyID(),
		"an Alchemy chain with a policy and no opt-out sponsors")

	optedOut := alchemy()
	optedOut.DisableGasSponsorship = true
	require.Empty(t, optedOut.SponsorshipPolicyID(),
		"an explicit opt-out wins over a configured policy — this is what keeps a local run off the production policy")

	// bnb-mainnet is configured exactly this way. Sponsorship is requested with
	// alchemy_requestGasAndPaymasterAndData, which a self-hosted Voltaire
	// bundler does not implement, so asking would fail the operation rather
	// than sponsor it.
	selfHosted := alchemy()
	selfHosted.BundlerProvider = BundlerProviderSelfHosted
	selfHosted.BundlerURL = "http://bundler.internal"
	require.Empty(t, selfHosted.SponsorshipPolicyID(),
		"a policy is inert on a non-Alchemy bundler and must not be attempted")

	noPolicy := alchemy()
	noPolicy.AlchemyPaymasterPolicyID = ""
	require.Empty(t, noPolicy.SponsorshipPolicyID())

	var nilConfig *SmartWalletConfig
	require.Empty(t, nilConfig.SponsorshipPolicyID(), "must be nil-safe")
}
