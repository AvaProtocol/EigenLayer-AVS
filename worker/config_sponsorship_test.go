package worker

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// sponsors resolves sponsorship the way the worker does at startup: through the
// SmartWalletConfig the send path is handed, not a parallel predicate. Keeping
// the test on the same path as production is the point — a second way to answer
// "will this sponsor?" is how the gateway and worker drifted apart to begin with.
func sponsors(t *testing.T, cfg *WorkerConfig) bool {
	t.Helper()
	smartWalletConfig, err := cfg.ToSmartWalletConfig()
	require.NoError(t, err)
	return smartWalletConfig.SponsorshipPolicyID() != ""
}

// A worker that cannot request sponsorship sends operations the smart wallet
// has to pay for itself — and a wallet holding only tokens cannot, so the
// user's withdrawal fails. That is #722, and it happened because the policy
// existed on the gateway's config and nowhere else. These tests pin the
// plumbing so the two cannot drift apart again.

func TestWorkerCarriesTheGasManagerPolicyIntoSmartWalletConfig(t *testing.T) {
	t.Setenv("ALCHEMY_PAYMASTER_POLICY_ID", "")
	t.Setenv("ALCHEMY_GAS_POLICY_ID", "")
	t.Setenv("GAS_MANAGER_WEBHOOK_SECRET", "")

	cfg := &WorkerConfig{
		ChainID:                  11155111,
		AlchemyPaymasterPolicyID: "policy-uuid",
		GasManagerWebhookSecret:  "shhh",
	}
	require.True(t, sponsors(t, cfg))

	smartWalletConfig, err := cfg.ToSmartWalletConfig()
	require.NoError(t, err)
	require.Equal(t, "policy-uuid", smartWalletConfig.AlchemyPaymasterPolicyID,
		"without this, priceOperationV07 never asks Alchemy to sponsor")
	require.Equal(t, "shhh", smartWalletConfig.GasManagerWebhookSecret,
		"echoed to Alchemy as webhookData; the gateway's webhook rejects a mismatch")
}

// The regression itself: a worker with no policy configured must report that
// it cannot sponsor, rather than looking fine and failing at send time.
func TestWorkerWithoutAPolicyReportsItCannotSponsor(t *testing.T) {
	t.Setenv("ALCHEMY_PAYMASTER_POLICY_ID", "")
	t.Setenv("ALCHEMY_GAS_POLICY_ID", "")

	cfg := &WorkerConfig{ChainID: 11155111}
	require.False(t, sponsors(t, cfg))

	smartWalletConfig, err := cfg.ToSmartWalletConfig()
	require.NoError(t, err)
	require.Empty(t, smartWalletConfig.AlchemyPaymasterPolicyID,
		"an empty policy is what sends send_v07 down the self-funded prefund path")
}

func TestWorkerPolicyResolutionMatchesTheGateway(t *testing.T) {
	t.Run("legacy yaml alias is accepted", func(t *testing.T) {
		t.Setenv("ALCHEMY_PAYMASTER_POLICY_ID", "")
		t.Setenv("ALCHEMY_GAS_POLICY_ID", "")
		cfg := &WorkerConfig{ChainID: 1, GasManagerPolicyID: "legacy-uuid"}
		smartWalletConfig, err := cfg.ToSmartWalletConfig()
		require.NoError(t, err)
		require.Equal(t, "legacy-uuid", smartWalletConfig.AlchemyPaymasterPolicyID,
			"renaming the key must not silently drop sponsorship on an existing deployment")
	})

	t.Run("env fills in when yaml is empty", func(t *testing.T) {
		t.Setenv("ALCHEMY_PAYMASTER_POLICY_ID", "env-uuid")
		t.Setenv("GAS_MANAGER_WEBHOOK_SECRET", "env-secret")
		cfg := &WorkerConfig{ChainID: 1}
		require.True(t, sponsors(t, cfg))
		smartWalletConfig, err := cfg.ToSmartWalletConfig()
		require.NoError(t, err)
		require.Equal(t, "env-uuid", smartWalletConfig.AlchemyPaymasterPolicyID)
		require.Equal(t, "env-secret", smartWalletConfig.GasManagerWebhookSecret)
	})

	t.Run("canonical yaml wins over the legacy alias", func(t *testing.T) {
		t.Setenv("ALCHEMY_PAYMASTER_POLICY_ID", "")
		t.Setenv("ALCHEMY_GAS_POLICY_ID", "")
		cfg := &WorkerConfig{ChainID: 1, AlchemyPaymasterPolicyID: "canonical", GasManagerPolicyID: "legacy"}
		smartWalletConfig, err := cfg.ToSmartWalletConfig()
		require.NoError(t, err)
		require.Equal(t, "canonical", smartWalletConfig.AlchemyPaymasterPolicyID)
	})

	t.Run("surrounding whitespace is trimmed", func(t *testing.T) {
		t.Setenv("ALCHEMY_PAYMASTER_POLICY_ID", "")
		t.Setenv("ALCHEMY_GAS_POLICY_ID", "")
		cfg := &WorkerConfig{ChainID: 1, AlchemyPaymasterPolicyID: "  padded  ", GasManagerWebhookSecret: " s "}
		smartWalletConfig, err := cfg.ToSmartWalletConfig()
		require.NoError(t, err)
		require.Equal(t, "padded", smartWalletConfig.AlchemyPaymasterPolicyID,
			"a policy id with stray whitespace would be rejected by Alchemy as an unknown policy")
		require.Equal(t, "s", smartWalletConfig.GasManagerWebhookSecret)
	})
}

// A worker that must not draw on the production policy says so explicitly.
//
// The policy's custom-rules webhook is a single URL pointing at the PRODUCTION
// gateway, so a locally-run worker requesting sponsorship has production
// approve it — deciding against production's wallet records and billing
// production's budget for a laptop. The opt-out is a dedicated setting rather
// than an inference from the logging environment, so a logging change can
// never move money.
func TestDisableGasSponsorshipDropsAConfiguredPolicy(t *testing.T) {
	t.Setenv("ALCHEMY_PAYMASTER_POLICY_ID", "")
	t.Setenv("ALCHEMY_GAS_POLICY_ID", "")

	cfg := &WorkerConfig{
		ChainID:                  11155111,
		BundlerProvider:          "alchemy",
		AlchemyAPIKey:            "key",
		AlchemyPaymasterPolicyID: "policy-uuid",
		GasManagerWebhookSecret:  "shhh",
		DisableGasSponsorship:    true,
	}
	require.False(t, sponsors(t, cfg))

	smartWalletConfig, err := cfg.ToSmartWalletConfig()
	require.NoError(t, err)
	require.Empty(t, smartWalletConfig.SponsorshipPolicyID(),
		"an explicitly configured policy must still be dropped when opted out")
}

// The accident the opt-out guards against: the policy is never written into a
// local config file, it just exists in the developer's environment.
func TestOptOutIgnoresAPolicyInheritedFromTheEnvironment(t *testing.T) {
	t.Setenv("ALCHEMY_PAYMASTER_POLICY_ID", "leaked-from-shell")

	cfg := &WorkerConfig{
		ChainID:               11155111,
		BundlerProvider:       "alchemy",
		AlchemyAPIKey:         "key",
		DisableGasSponsorship: true,
	}
	require.False(t, sponsors(t, cfg))

	cfg.DisableGasSponsorship = false
	require.True(t, sponsors(t, cfg),
		"the opt-out must be what disables it, not sponsorship being broken outright")
}

// bnb-mainnet runs a self-hosted bundler. Sponsorship there would send an
// Alchemy-only method to Voltaire and fail the operation, so the worker must
// report that it cannot sponsor even though a policy is configured.
func TestSelfHostedBundlerCannotSponsor(t *testing.T) {
	t.Setenv("ALCHEMY_PAYMASTER_POLICY_ID", "")
	t.Setenv("ALCHEMY_GAS_POLICY_ID", "")

	cfg := &WorkerConfig{
		ChainID:                  56,
		BundlerProvider:          "self_hosted",
		BundlerURL:               "http://bundler.internal",
		AlchemyPaymasterPolicyID: "policy-uuid",
	}
	require.False(t, sponsors(t, cfg),
		"a policy on a self-hosted bundler is inert; attempting it would fail the send")
}
