package worker

import (
	"testing"

	"github.com/stretchr/testify/require"
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
