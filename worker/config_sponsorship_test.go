package worker

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
)

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
	require.True(t, cfg.SponsorshipConfigured())

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
	require.False(t, cfg.SponsorshipConfigured())

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
		require.True(t, cfg.SponsorshipConfigured())
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

// A development process must never draw on the Gas Manager policy.
//
// The policy's custom-rules webhook is a single URL pointing at the PRODUCTION
// gateway, so a locally-run worker that requests sponsorship has production
// approve it — deciding against production's wallet records and charging
// production's credit ledger for a laptop. Refused in code rather than by
// convention, because the policy id also resolves from the environment: an
// exported ALCHEMY_PAYMASTER_POLICY_ID is enough to opt in by accident.
func TestDevelopmentRefusesSponsorshipEvenWhenConfigured(t *testing.T) {
	t.Setenv("ALCHEMY_PAYMASTER_POLICY_ID", "")
	t.Setenv("ALCHEMY_GAS_POLICY_ID", "")

	cfg := &WorkerConfig{
		ChainID:                  11155111,
		Environment:              "development",
		AlchemyPaymasterPolicyID: "policy-uuid",
		GasManagerWebhookSecret:  "shhh",
	}
	require.False(t, cfg.SponsorshipConfigured(),
		"a development worker must not report itself as able to sponsor")

	smartWalletConfig, err := cfg.ToSmartWalletConfig()
	require.NoError(t, err)
	require.Empty(t, smartWalletConfig.AlchemyPaymasterPolicyID,
		"an explicitly configured policy must still be dropped in development")
}

// The accident this guards against: the policy is never written to a local
// config file, it just exists in the developer's environment.
func TestDevelopmentIgnoresAPolicyInheritedFromTheEnvironment(t *testing.T) {
	t.Setenv("ALCHEMY_PAYMASTER_POLICY_ID", "leaked-from-shell")

	cfg := &WorkerConfig{ChainID: 11155111, Environment: "development"}
	require.False(t, cfg.SponsorshipConfigured())

	smartWalletConfig, err := cfg.ToSmartWalletConfig()
	require.NoError(t, err)
	require.Empty(t, smartWalletConfig.AlchemyPaymasterPolicyID)

	// Same worker, production environment: the policy applies.
	cfg.Environment = "production"
	require.True(t, cfg.SponsorshipConfigured())
	smartWalletConfig, err = cfg.ToSmartWalletConfig()
	require.NoError(t, err)
	require.Equal(t, "leaked-from-shell", smartWalletConfig.AlchemyPaymasterPolicyID,
		"the guard must key on the environment, not disable sponsorship outright")
}

// Only development is refused. Anything else — production, staging, an unset
// value — keeps whatever sponsorship it was given, so the guard cannot quietly
// switch a deployed chain to self-funded.
func TestOnlyDevelopmentIsRefused(t *testing.T) {
	for _, env := range []string{"production", "Production", "staging", ""} {
		require.False(t, config.SponsorshipRefusedForEnvironment(env), "env %q", env)
	}
	for _, env := range []string{"development", "Development", "  development  "} {
		require.True(t, config.SponsorshipRefusedForEnvironment(env), "env %q", env)
	}
}
