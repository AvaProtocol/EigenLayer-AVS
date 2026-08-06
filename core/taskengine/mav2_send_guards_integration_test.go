//go:build integration
// +build integration

package taskengine

// Sepolia live checks for the MA v2 send-path guards introduced after the
// AA23 factory-mismatch hand-off. These prove the gateway refuses doomed
// UserOps BEFORE the bundler returns opaque AA23.
//
// They dial a public Sepolia RPC only — no bundler, no private keys, no
// balance. Failures here mean the guard regressed, not that Sepolia is flaky.
//
//	make test/integration
//
// Optional env:
//
//	SPIKE_RPC_URL  — defaults to https://ethereum-sepolia-rpc.publicnode.com

import (
	"context"
	"math/big"
	"os"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
)

// Live Sepolia fixtures from the 2026-08-05 Auto-mode AA23 investigation
// (owner 0xc60e… holds both a v0.6 SimpleAccount and an MA v2 runner at salt 0).
const (
	sepoliaOwnerEOA         = "0xc60e71bd0f2e6d8832Fea1a2d56091C48493C788"
	sepoliaMAv2Runner       = "0x46aD59AFc8a21F0dfa3FE74C53A998b7550c7BDd"
	sepoliaSimpleAccountV06 = "0x5d814Cc9E94B2656f59Ee439D44AA1b6ca21434f"
)

func sepoliaRPC(t *testing.T) string {
	t.Helper()
	if u := os.Getenv("SPIKE_RPC_URL"); u != "" {
		return u
	}
	return "https://ethereum-sepolia-rpc.publicnode.com"
}

func sepoliaMAv2Config(t *testing.T) *config.SmartWalletConfig {
	t.Helper()
	return &config.SmartWalletConfig{
		EthRpcUrl:       sepoliaRPC(t),
		AccountProvider: config.AccountProviderModularAccountV2,
		// Deliberately leave the raw v0.6 default factory — EffectiveFactory
		// must still resolve to the MA v2 constant (the findings red herring).
		FactoryAddress:  common.HexToAddress(config.DefaultFactoryProxyAddressHex),
		ChainID:         11155111,
		BundlerProvider: config.BundlerProviderSelfHosted,
		// No bundler URL: guards must fail before dialing the bundler.
	}
}

func TestMAv2SendRejectsMissingSessionGrant_Sepolia(t *testing.T) {
	swCfg := sepoliaMAv2Config(t)
	owner := common.HexToAddress(sepoliaOwnerEOA)
	salt := big.NewInt(0)
	runner := common.HexToAddress(sepoliaMAv2Runner)

	// Empty calldata is fine — we never reach execution.
	callData, err := aa.PackExecute(owner, big.NewInt(0), nil)
	require.NoError(t, err)

	// No session resolver installed → resolveSession returns nil → hard fail.
	preset.SetSessionResolver(nil)
	t.Cleanup(func() { preset.SetSessionResolver(nil) })

	_, _, err = preset.SendUserOpMAv2(swCfg, owner, callData, &runner, salt, nil, logger.NewNoOpLogger())
	require.Error(t, err, "MA v2 send without a session grant must fail before the bundler")
	require.Contains(t, err.Error(), "no session authorization",
		"error must name the missing grant, not AA23: %v", err)
	require.NotContains(t, strings.ToLower(err.Error()), "aa23",
		"must not reach bundler estimation: %v", err)
	require.Contains(t, err.Error(), runner.Hex())
}

func TestMAv2SendRejectsSimpleAccountRunner_Sepolia(t *testing.T) {
	swCfg := sepoliaMAv2Config(t)
	owner := common.HexToAddress(sepoliaOwnerEOA)
	salt := big.NewInt(0)
	v06Runner := common.HexToAddress(sepoliaSimpleAccountV06)

	// Prove on-chain that salt-0 under the MA v2 factory is the OTHER address.
	chain, err := ethclient.Dial(swCfg.EthRpcUrl)
	require.NoError(t, err)
	defer chain.Close()

	factory, err := aa.EffectiveFactory(swCfg)
	require.NoError(t, err)
	require.Equal(t, aa.MAv2FactoryAddress(), factory)

	derived, err := aa.DeriveSenderAddressAuto(chain, owner, factory, salt)
	require.NoError(t, err)
	require.NotNil(t, derived)
	require.Equal(t, strings.ToLower(sepoliaMAv2Runner), strings.ToLower(derived.Hex()),
		"fixture drift: owner salt-0 under MA v2 factory should be the known MA v2 runner")
	require.NotEqual(t, *derived, v06Runner,
		"fixture drift: v0.6 runner must not equal the MA v2 derivation")

	// Confirm the v0.6 runner still has code (fixture has not been self-destructed).
	code, err := chain.CodeAt(context.Background(), v06Runner, nil)
	require.NoError(t, err)
	require.NotEmpty(t, code, "fixture drift: v0.6 SimpleAccount %s should be deployed", v06Runner.Hex())

	// Explicit session auth so the missing-grant guard does not fire first —
	// we want the account-type / derive mismatch.
	controllerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	auth := &preset.SessionAuthorization{EntityID: 1, SignerKey: controllerKey}

	callData, err := aa.PackExecute(owner, big.NewInt(0), nil)
	require.NoError(t, err)

	_, _, err = preset.SendUserOpMAv2(swCfg, owner, callData, &v06Runner, salt, auth, logger.NewNoOpLogger())
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not match the address derived",
		"v0.6 SimpleAccount runner must be refused with a derive mismatch: %v", err)
	require.Contains(t, err.Error(), v06Runner.Hex())
	require.Contains(t, err.Error(), derived.Hex())
	require.NotContains(t, strings.ToLower(err.Error()), "aa23",
		"must not reach bundler estimation: %v", err)
}

func TestMAv2SendRejectsUndeployedWrongSalt_Sepolia(t *testing.T) {
	swCfg := sepoliaMAv2Config(t)
	owner := common.HexToAddress(sepoliaOwnerEOA)
	// Claim the salt-0 MA v2 address while deriving initCode for salt 99.
	runner := common.HexToAddress(sepoliaMAv2Runner)
	wrongSalt := big.NewInt(99)

	controllerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	auth := &preset.SessionAuthorization{EntityID: 1, SignerKey: controllerKey}

	callData, err := aa.PackExecute(owner, big.NewInt(0), nil)
	require.NoError(t, err)

	_, _, err = preset.SendUserOpMAv2(swCfg, owner, callData, &runner, wrongSalt, auth, logger.NewNoOpLogger())
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not match the address derived",
		"salt mismatch must refuse before initCode attach: %v", err)
	require.NotContains(t, strings.ToLower(err.Error()), "aa23")
}

func TestMAv2EffectiveFactoryOnSepoliaConfig_IgnoresRawV06Factory(t *testing.T) {
	swCfg := sepoliaMAv2Config(t)
	factory, err := aa.EffectiveFactory(swCfg)
	require.NoError(t, err)
	require.Equal(t, aa.MAv2FactoryAddress(), factory)
	require.NotEqual(t, swCfg.FactoryAddress, factory,
		"raw config factory is the v0.6 default; effective must be MA v2")
}

// The 16:31 Auto swap failure was diagnosed as AA23, but a live re-probe of
// the same grant + atomicBatch shape estimates successfully and only fails
// later on prefund. This test locks the early fail-fast: zero balance + no
// Gas Manager must refuse BEFORE the bundler, with a sponsorship hint.
func TestMAv2SendRejectsZeroBalanceWithoutGasManager_Sepolia(t *testing.T) {
	swCfg := sepoliaMAv2Config(t)
	// No AlchemyPaymasterPolicyID — matches the local gateway log
	// "paymaster_policy_set: false".
	owner := common.HexToAddress(sepoliaOwnerEOA)
	runner := common.HexToAddress(sepoliaMAv2Runner)
	salt := big.NewInt(0)

	// Confirm fixture is still unfunded / counterfactual.
	chain, err := ethclient.Dial(swCfg.EthRpcUrl)
	require.NoError(t, err)
	defer chain.Close()
	bal, err := chain.BalanceAt(context.Background(), runner, nil)
	require.NoError(t, err)
	require.Equal(t, 0, bal.Sign(), "fixture drift: MA v2 runner should be unfunded for this guard test")
	code, err := chain.CodeAt(context.Background(), runner, nil)
	require.NoError(t, err)
	require.Empty(t, code, "fixture drift: MA v2 runner should still be counterfactual")

	controllerKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	// Minimal auth so we pass the session-grant guard and reach prefund.
	// (A real deferred grant is not required to exercise the balance check.)
	auth := &preset.SessionAuthorization{EntityID: 1, SignerKey: controllerKey}

	callData, err := aa.PackExecute(owner, big.NewInt(0), nil)
	require.NoError(t, err)

	_, _, err = preset.SendUserOpMAv2(swCfg, owner, callData, &runner, salt, auth, logger.NewNoOpLogger())
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot pay gas",
		"zero-balance unsponsored MA v2 ops must fail with a prefund message: %v", err)
	require.Contains(t, err.Error(), "ALCHEMY_PAYMASTER_POLICY_ID",
		"error must point at Alchemy paymaster policy sponsorship: %v", err)
	require.NotContains(t, strings.ToLower(err.Error()), "aa23",
		"must not reach bundler estimate: %v", err)
}
