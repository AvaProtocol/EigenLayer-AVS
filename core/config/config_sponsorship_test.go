package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

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

// A policy on a non-Alchemy bundler is refused at load rather than silently
// running self-funded. Sponsorship is requested with
// alchemy_requestGasAndPaymasterAndData, which a self-hosted Voltaire bundler
// does not implement — so the config promises sponsorship the chain cannot
// deliver, and the first symptom would be a user's operation failing for a
// wallet nobody thought needed a gas balance.
func TestSponsorshipValidationRefusesANonAlchemyBundler(t *testing.T) {
	withPolicy := func(provider string) *SmartWalletConfig {
		return &SmartWalletConfig{
			ChainID:                  56,
			BundlerProvider:          provider,
			BundlerURL:               "http://bundler.internal",
			AlchemyAPIKey:            "key",
			AlchemyPaymasterPolicyID: "policy",
		}
	}

	err := withPolicy(BundlerProviderSelfHosted).ValidateSponsorship()
	require.Error(t, err)
	require.Contains(t, err.Error(), BundlerProviderAlchemy, "the error must name the fix")
	require.Contains(t, err.Error(), "disable_gas_sponsorship", "and the other way out")

	require.NoError(t, withPolicy(BundlerProviderAlchemy).ValidateSponsorship())

	// An explicit opt-out settles it: the policy is not to be used, so which
	// bundler serves the chain no longer matters.
	optedOut := withPolicy(BundlerProviderSelfHosted)
	optedOut.DisableGasSponsorship = true
	require.NoError(t, optedOut.ValidateSponsorship(),
		"an opted-out chain must still boot on a self-hosted bundler")

	// No policy, no promise to break.
	noPolicy := withPolicy(BundlerProviderSelfHosted)
	noPolicy.AlchemyPaymasterPolicyID = ""
	require.NoError(t, noPolicy.ValidateSponsorship())

	var nilConfig *SmartWalletConfig
	require.NoError(t, nilConfig.ValidateSponsorship(), "must be nil-safe")
}

// Gateway chains inherit sponsorship from the top-level config, so both the
// opt-out and the validation have to account for that — checking a chain
// before it inherits only ever sees an empty policy and passes.
//
// This is the shape production actually runs (gateway mode, several chains)
// and the shape local development runs, so a hole here is a hole everywhere.
func TestChainsInheritSponsorshipAndAreValidatedAfterwards(t *testing.T) {
	t.Run("a chain inherits the opt-out, not just the policy", func(t *testing.T) {
		// Reproduces the hole: inheriting the policy alone left every chain
		// sponsoring even though the gateway had opted out.
		chain := &SmartWalletConfig{ChainID: 11155111, BundlerProvider: BundlerProviderAlchemy, AlchemyAPIKey: "k"}
		chain.AlchemyPaymasterPolicyID = "policy"
		chain.DisableGasSponsorship = true
		require.Empty(t, chain.SponsorshipPolicyID(),
			"an opted-out gateway must not sponsor on any of its chains")
	})

	t.Run("validation sees the inherited policy", func(t *testing.T) {
		// Before inheritance a self-hosted chain looks fine, because it carries
		// no policy of its own. It is only a misconfiguration once it inherits.
		chain := &SmartWalletConfig{
			ChainID: 56, BundlerProvider: BundlerProviderSelfHosted, BundlerURL: "http://bundler.internal",
		}
		require.NoError(t, chain.ValidateSponsorship(), "nothing to object to yet")

		chain.AlchemyPaymasterPolicyID = "policy" // as the gateway pushes down
		require.Error(t, chain.ValidateSponsorship(),
			"once inherited, a policy on a self-hosted bundler must be refused")
	})
}
