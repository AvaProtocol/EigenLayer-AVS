//go:build integration
// +build integration

package taskengine

// End-to-end proof of the production MA v2 path: an owner authorizes a session
// grant with ONE off-chain signature, and the gateway then deploys the
// account, installs the grant during validation, and executes — all in one
// operation it signs itself.
//
// This is the folded-in form of scripts/spike/deferred_action, promoted from a
// hand-run harness so the flow is protected by the suite. It exercises the
// public API (aa.SessionGrant, preset.SessionAuthorization,
// preset.SendUserOpMAv2) rather than harness internals, so it fails when the
// contract the gateway depends on changes.
//
// Prerequisites are REQUIRED, not skipped: a test that silently skips when a
// key is missing reports success for a path it never ran. Run with:
//
//	make test/integration
//
// Env: SPIKE_OWNER_KEY, SPIKE_CONTROLLER_KEY, SPIKE_BUNDLER_URL,
//      SPIKE_RPC_URL (optional), SPIKE_SALT (optional, default 9).

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/userop"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
)

// requireKey loads a private key that the test cannot run without.
func requireKey(t *testing.T, name string) (*ecdsa.PrivateKey, common.Address) {
	t.Helper()
	raw := os.Getenv(name)
	require.NotEmpty(t, raw, "%s must be set — this test executes real operations and cannot be faked", name)
	key, err := crypto.HexToECDSA(strings.TrimPrefix(raw, "0x"))
	require.NoError(t, err, "%s is not a valid hex private key", name)
	return key, crypto.PubkeyToAddress(key.PublicKey)
}

func TestMAv2SessionGrantEndToEnd(t *testing.T) {
	ownerKey, owner := requireKey(t, "SPIKE_OWNER_KEY")
	controllerKey, controller := requireKey(t, "SPIKE_CONTROLLER_KEY")

	bundlerURL := os.Getenv("SPIKE_BUNDLER_URL")
	require.NotEmpty(t, bundlerURL, "SPIKE_BUNDLER_URL must be set")

	rpcURL := os.Getenv("SPIKE_RPC_URL")
	if rpcURL == "" {
		rpcURL = "https://ethereum-sepolia-rpc.publicnode.com"
	}
	salt := big.NewInt(9)
	if s := os.Getenv("SPIKE_SALT"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		require.NoError(t, err, "SPIKE_SALT must be a decimal integer")
		salt = big.NewInt(n)
	}

	ctx := context.Background()
	chain, err := ethclient.Dial(rpcURL)
	require.NoError(t, err, "cannot reach the chain RPC")
	defer chain.Close()

	chainID, err := chain.ChainID(ctx)
	require.NoError(t, err)

	swCfg := &config.SmartWalletConfig{
		EthRpcUrl:            rpcURL,
		BundlerURL:           bundlerURL,
		BundlerProvider:      config.BundlerProviderSelfHosted, // BundlerURL is used verbatim
		AccountProvider:      config.AccountProviderModularAccountV2,
		ChainID:              chainID.Int64(),
		ControllerPrivateKey: controllerKey,
		FactoryAddress:       common.HexToAddress(config.DefaultFactoryProxyAddressHex),
	}

	factory, err := aa.EffectiveFactory(swCfg)
	require.NoError(t, err)
	require.Equal(t, aa.MAv2FactoryAddress(), factory,
		"an MA v2 chain must resolve to the MA v2 factory")

	account, err := aa.DeriveSenderAddressAuto(chain, owner, factory, salt)
	require.NoError(t, err, "deriving the account address")
	t.Logf("owner=%s controller=%s account=%s salt=%s",
		owner.Hex(), controller.Hex(), account.Hex(), salt)

	code, err := chain.CodeAt(ctx, *account, nil)
	require.NoError(t, err)
	require.Empty(t, code,
		"account %s is already deployed — this test proves the FIRST operation, so use an unused SPIKE_SALT",
		account.Hex())

	balance, err := chain.BalanceAt(ctx, *account, nil)
	require.NoError(t, err)
	require.Positive(t, balance.Sign(),
		"account %s holds no native balance; fund it before running (it pays its own gas here)",
		account.Hex())

	// ---- the owner's single, off-chain authorization -------------------------
	//
	// Signed before the operation exists: no gas price, no deployment, nothing
	// on chain. That property is what lets the grant screen collect it.

	const entity = aa.MinSessionEntityID
	installCall, err := aa.PackSessionSignerInstall(aa.SessionGrant{
		EntityID: entity,
		Signer:   controller,
		Global:   true,
		// No execution hook here, so this grant can administer itself. That is
		// acceptable for a test fixture and deliberately loud; production
		// grants carry hooks or selector scoping.
		AllowSelfAdministration: true,
	})
	require.NoError(t, err, "packing the install call")

	deadline := uint64(time.Now().Add(2 * time.Hour).Unix())

	// The digest commits to the FULL nonce of the operation that will carry
	// the action, which is knowable in advance only because a fresh entity is
	// a fresh nonce key whose sequence is zero.
	carrierNonce, err := userop.EncodeNonceMAv2(entity,
		userop.ValidationOptionGlobal|userop.ValidationOptionDeferredAction, 0)
	require.NoError(t, err)

	digest, err := userop.DeferredActionDigest(chainID, *account, carrierNonce, deadline, installCall)
	require.NoError(t, err, "building the EIP-712 digest")

	ownerSig, err := crypto.Sign(digest.Bytes(), ownerKey) // raw digest — not EIP-191 wrapped
	require.NoError(t, err)
	ownerSig[64] += 27

	deferredData, err := userop.EncodeDeferredActionData(userop.FallbackSignerLocator(), deadline, installCall)
	require.NoError(t, err)

	// ---- the gateway executes, signing as the controller ---------------------

	// The applied-marking callback the resolver installs in production —
	// captured here to prove the send path invokes it with the carrying
	// operation's hash once the receipt is in hand.
	var appliedHash string
	auth := &preset.SessionAuthorization{
		EntityID:       entity,
		SignerKey:      controllerKey,
		DeferredData:   deferredData,
		OwnerSignature: ownerSig,
		CarrierNonce:   carrierNonce, // send path asserts op.Nonce == this
		OnApplied: func(userOpHash string) error {
			appliedHash = userOpHash
			return nil
		},
	}
	require.NoError(t, auth.Validate())
	require.True(t, auth.Deferred(), "this operation must carry the install")

	callData, err := aa.PackExecute(owner, big.NewInt(0), nil)
	require.NoError(t, err, "packing the executed call")

	op, receipt, err := preset.SendUserOpMAv2(swCfg, owner, callData, account, salt, auth, logger.NewNoOpLogger())
	require.NoError(t, err, "the grant-carrying operation must succeed")
	require.NotNil(t, receipt, "no receipt — the operation did not mine")
	require.NotNil(t, op)
	require.NotNil(t, op.Factory, "the first operation must also deploy the account")
	require.NotEmpty(t, appliedHash,
		"the send path must report the applied install so the stored grant stops attaching it")

	deployed, err := chain.CodeAt(ctx, *account, nil)
	require.NoError(t, err)
	require.NotEmpty(t, deployed, "the account should exist after its first operation")

	// ---- the point: the controller now has authority it did not have ---------

	followUp := &preset.SessionAuthorization{EntityID: entity, SignerKey: controllerKey}
	require.False(t, followUp.Deferred(), "the install must not be replayed")

	op2, receipt2, err := preset.SendUserOpMAv2(swCfg, owner, callData, account, salt, followUp, logger.NewNoOpLogger())
	require.NoError(t, err,
		"the controller must be able to sign on its own once the grant is installed")
	require.NotNil(t, receipt2, "the follow-up operation did not mine")
	require.Nil(t, op2.Factory, "the account already exists; this must not redeploy")
}
