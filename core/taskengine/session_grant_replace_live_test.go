//go:build integration
// +build integration

package taskengine

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// Replacing a grant, end to end, through the code that actually runs in
// production — PrepareSessionPolicy → SubmitSessionPolicy → a real operation
// → the resolver's teardown verification.
//
// scripts/spike/deferred_replace already proved the MECHANISM composes on
// Sepolia. It did so with hand-built payloads; this proves the GATEWAY builds
// the same thing and that the prior entity is genuinely gone afterwards. The
// two failures it is here to catch are ones no offline test can see: a batch
// the account rejects, and a batch it accepts while clearing nothing.
//
// Behind the `integration` build tag with the rest of the live suite (#727).
// Each run also consumes two validation entities on its runner, and entities
// cannot be recycled (#719), so this is an on-demand check rather than a
// per-PR one even among live tests.
//
//	SPIKE-style env: OWNER_EOA, TEST_PRIVATE_KEY, config/test.yaml.
//	go test ./core/taskengine -run TestGrantReplaceClearsThePriorEntity_Sepolia -v
func TestGrantReplaceClearsThePriorEntity_Sepolia(t *testing.T) {
	cfg, err := config.NewConfig(testutil.GetConfigPath(testutil.DefaultConfigPath))
	require.NoError(t, err, "config/test.yaml must load")

	client, err := ethclient.Dial(cfg.SmartWallet.EthRpcUrl)
	require.NoError(t, err, "cannot reach the configured RPC; a live test with no chain proves nothing")
	t.Cleanup(func() { client.Close() })

	chainID, err := client.ChainID(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(11155111), chainID.Int64(),
		"this check is Sepolia-specific; skipping on a misconfigured RPC would report green while proving nothing")

	ownerHex := strings.TrimSpace(os.Getenv("OWNER_EOA"))
	require.NotEmpty(t, ownerHex, "OWNER_EOA must be set: the grant is authorized by that owner")
	owner := common.HexToAddress(ownerHex)

	ownerKey := requireOwnerKey(t)
	require.Equal(t, owner, crypto.PubkeyToAddress(ownerKey.PublicKey),
		"TEST_PRIVATE_KEY must be OWNER_EOA's key — only the owner can authorize a grant")

	setGlobalFactory(t, cfg.SmartWallet)
	runner, err := aa.GetSenderAddressMAv2(client, owner, big.NewInt(fixtureSaltGrantReplace))
	require.NoError(t, err)
	t.Logf("runner %s (salt %d)", runner.Hex(), fixtureSaltGrantReplace)

	// Operations here are self-funded: the fixture is not wired to the Gas
	// Manager policy, and pointing a test at it would have production approve
	// and bill the run.
	requireFundedRunner(t, cfg.SmartWallet, *runner, big.NewInt(3_000_000_000_000_000)) // 0.003 ETH

	// An account with no code answers every signers() read with zero, so the
	// entity assertions below would all pass while proving nothing. Deploying
	// is fixture setup, not this test's subject — fail loudly instead.
	code, err := client.CodeAt(context.Background(), *runner, nil)
	require.NoError(t, err)
	require.NotEmpty(t, code,
		"runner %s is not deployed; deploy it first — an undeployed account reads every entity as clear",
		runner.Hex())

	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })
	engine := New(db, cfg, nil, testutil.GetLogger())
	t.Cleanup(engine.Stop)
	engine.InstallSessionResolver()
	t.Cleanup(func() { preset.SetSessionResolver(nil) })

	factoryAddr := effectiveFactoryAddr(t, cfg.SmartWallet)
	require.NoError(t, StoreWallet(db, chainID.Int64(), owner, &model.SmartWallet{
		Owner: &owner, Address: runner, Factory: &factoryAddr,
		Salt: big.NewInt(fixtureSaltGrantReplace),
	}))
	user := &model.User{Address: owner, SmartAccountAddress: runner}
	seedEntitiesConsumedOnChain(t, db, client, cfg.SmartWallet, ownerKey, chainID.Int64(), owner, *runner)

	// ── 1. First grant. Nothing installed yet, so the payload is a plain
	// install and the entity it claims must be free on chain.
	first := grantThroughEngine(t, engine, user, ownerKey, *runner)
	require.Empty(t, firstPrepared(t, engine, user).Supersedes,
		"precondition: nothing installed to supersede")
	requireEntityClear(t, client, *runner, first.EntityID, true)

	// ── 2. Land it. The install rides this operation, so afterwards the
	// entity is real and the next grant has something to replace.
	runOperationUnderGrant(t, engine, user, *runner)
	requireEntityClear(t, client, *runner, first.EntityID, false)
	t.Logf("grant %s installed at entity %d", first.ID, first.EntityID)

	// ── 3. Replace it. This is the assertion that matters: the gateway must
	// now build a batch, not a bare install.
	prepared := firstPrepared(t, engine, user)
	require.Len(t, prepared.Supersedes, 1, "an installed grant must be superseded on chain too")
	require.Equal(t, first.EntityID, prepared.Supersedes[0].EntityID)

	second := submitPrepared(t, engine, user, ownerKey, prepared)
	require.NotEqual(t, first.EntityID, second.EntityID, "the replacement takes a fresh entity")

	// ── 4. Land the batch, then read the chain. A mined operation is not
	// evidence of teardown — the account catches onUninstall reverts and
	// strands module state, which is exactly the failure this test exists for.
	runOperationUnderGrant(t, engine, user, *runner)

	requireEntityClear(t, client, *runner, first.EntityID, true)
	requireEntityClear(t, client, *runner, second.EntityID, false)
	t.Logf("✅ entity %d cleared, entity %d holds the replacement", first.EntityID, second.EntityID)

	// And storage agrees: exactly one usable grant, the new one.
	usable := usableOn(t, db, owner, *runner)
	require.Len(t, usable, 1)
	require.Equal(t, second.ID, usable[0].ID)
}

// firstPrepared runs the production prepare for a standard grant.
func firstPrepared(t *testing.T, engine *Engine, user *model.User) *PreparedSessionGrant {
	t.Helper()
	prepared, err := engine.PrepareSessionPolicy(user, SessionPolicyInput{
		Wallet: *user.SmartAccountAddress, ChainID: 11155111,
		AgentLabel: "ReplaceLive", Justification: "live replace check",
		Permissions: livePermissions(),
	})
	require.NoError(t, err)
	return prepared
}

func submitPrepared(t *testing.T, engine *Engine, user *model.User,
	ownerKey *ecdsa.PrivateKey, prepared *PreparedSessionGrant) *model.SessionPolicy {
	t.Helper()
	sig, err := crypto.Sign(prepared.Digest.Bytes(), ownerKey)
	require.NoError(t, err)
	sig[64] += 27

	stored, _, err := engine.SubmitSessionPolicy(user, SessionPolicyInput{
		Wallet: *user.SmartAccountAddress, ChainID: 11155111,
		AgentLabel: "ReplaceLive", Justification: "live replace check",
		Permissions: livePermissions(),
	}, prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline, sig)
	require.NoError(t, err)
	return stored
}

func grantThroughEngine(t *testing.T, engine *Engine, user *model.User, ownerKey *ecdsa.PrivateKey, runner common.Address) *model.SessionPolicy {
	t.Helper()
	return submitPrepared(t, engine, user, ownerKey, firstPrepared(t, engine, user))
}

// runOperationUnderGrant sends one real operation so a pending grant's
// deferred action reaches the chain.
//
// It must be an operation the grant PERMITS. An ETH transfer to the owner
// AA23s here: the allowlist hook rejects a target the grant never named, and
// because the install rides this operation, a rejected call also means the
// grant never lands. approve() on the allowlisted token is the same shape
// production uses.
func runOperationUnderGrant(t *testing.T, engine *Engine, user *model.User, runner common.Address) {
	t.Helper()
	// Unique per call, so nothing here can pass on a previous run's allowance.
	approveAmount := big.NewInt(time.Now().Unix())
	result, err := engine.RunNodeImmediately("contractWrite", map[string]interface{}{
		"contractAddress": SEPOLIA_USDC,
		"contractAbi":     approveABIForReplace(),
		// Post-G5 the node itself must name its chain; only the RPC entry
		// stamps one from the request, and this calls the engine directly.
		"chainId": int64(11155111),
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName":   "approve",
				"methodParams": []interface{}{SEPOLIA_SWAPROUTER, approveAmount.String()},
			},
		},
	}, map[string]interface{}{
		"settings": map[string]interface{}{
			"runner": runner.Hex(), "smartWallet": runner.Hex(), "chain_id": int64(11155111),
		},
	}, user, false)
	require.NoError(t, err, "the operation carrying the grant must not error")
	require.NotNil(t, result)
	if success, ok := result["success"].(bool); ok && !success {
		t.Fatalf("operation failed: %v", result["error"])
	}
	// The install applies during validation; give the receipt path a moment to
	// record it before the next grant reads the chain.
	time.Sleep(2 * time.Second)
}

// requireEntityClear asserts an entity's on-chain occupancy. This is the only
// honest check: the receipt says the operation ran, not that the uninstall
// removed anything.
func requireEntityClear(t *testing.T, client *ethclient.Client, runner common.Address, entity uint32, wantClear bool) {
	t.Helper()
	signer, err := aa.EntitySignerOnChain(context.Background(), client, runner, entity)
	require.NoError(t, err, "reading entity %d", entity)
	if wantClear {
		require.Equal(t, common.Address{}, signer,
			"entity %d must be clear — a surviving entity is live authority the product believes is revoked", entity)
		return
	}
	require.NotEqual(t, common.Address{}, signer, "entity %d must be installed", entity)
}

// livePermissions is a production-shaped grant: allowlist + spend cap + time
// range, which is what makes this test meaningful. A bare global grant carries
// no hooks, and hooks are exactly what a replace has to tear down — the stale
// spend cap this whole change exists to remove.
func livePermissions() SessionPermissions {
	token := common.HexToAddress(SEPOLIA_USDC)
	return SessionPermissions{
		AllowedActions: []model.AllowedAction{
			{Target: &token, Selectors: []string{"0x095ea7b3"}}, // approve(address,uint256)
		},
		// Large enough that repeated runs never exhaust it; the cap is here to
		// give the entity hook state to strand, not to be tested for itself.
		SpendCap: &model.ERC20SpendCap{
			Token: &token, Amount: "1000000000000", GrantedCap: "1000000000000",
		},
		ValidUntilMs: time.Now().Add(7 * 24 * time.Hour).UnixMilli(),
	}
}

func approveABIForReplace() []interface{} {
	return []interface{}{
		map[string]interface{}{
			"inputs": []interface{}{
				map[string]interface{}{"name": "spender", "type": "address"},
				map[string]interface{}{"name": "amount", "type": "uint256"},
			},
			"name":            "approve",
			"outputs":         []interface{}{map[string]interface{}{"name": "", "type": "bool"}},
			"stateMutability": "nonpayable",
			"type":            "function",
		},
	}
}

// seedEntitiesConsumedOnChain makes storage reflect the entities this runner
// has already spent, so the allocator does not hand one out twice.
//
// NextSessionEntityID allocates from storage alone — max(entityID)+1 across the
// owner's policies, revoked ones included. That is correct in production, where
// a revoked policy is retained precisely so its entity is never reissued. It
// must never be reissued, and teardown is what makes that non-obvious: a
// torn-down entity reads clear on every module (signer, allowlist, spend cap,
// time range) while its EntryPoint deferred nonce key keeps its sequence
// forever. PrepareSessionGrant signs a carrier nonce at sequence 0 — knowable
// in advance only for a never-used key — so a reissued entity signs a nonce the
// EntryPoint already consumed, and the operation AA23s during validation with
// nothing on chain to explain why. See aa.EntryPointNonce.
//
// A live test starts from an empty database against a chain that remembers, so
// it restores that invariant itself. The record it writes is a real grant built
// by the production builder and signed by the real owner, marked revoked — the
// same thing storage would hold had the earlier run been this gateway.
func seedEntitiesConsumedOnChain(
	t *testing.T,
	db storage.Storage,
	client *ethclient.Client,
	swCfg *config.SmartWalletConfig,
	ownerKey *ecdsa.PrivateKey,
	chainID int64,
	owner, runner common.Address,
) {
	t.Helper()

	highest := uint32(0)
	for entity := uint32(1); entity <= 32; entity++ {
		sequence, err := aa.EntityDeferredNonceSequence(context.Background(), client, runner, entity)
		require.NoError(t, err, "reading the deferred nonce sequence of entity %d", entity)

		signer, err := aa.EntitySignerOnChain(context.Background(), client, runner, entity)
		require.NoError(t, err)

		if sequence != 0 || signer != (common.Address{}) {
			highest = entity
		}
	}
	if highest == 0 {
		return
	}

	// The allocator takes the maximum, so one record at the highest spent id
	// covers everything below it.
	controller := crypto.PubkeyToAddress(swCfg.ControllerPrivateKey.PublicKey)
	prepared, err := PrepareSessionGrant(db, chainID, controller, "seed-entities-consumed-on-chain",
		SessionGrantRequest{
			Owner: owner, Wallet: runner,
			AgentLabel:              "seed",
			AllowSelfAdministration: true,
		})
	require.NoError(t, err)
	prepared.Policy.EntityID = highest
	prepared.Policy.Status = model.SessionPolicyRevoked

	sig, err := crypto.Sign(prepared.Digest.Bytes(), ownerKey)
	require.NoError(t, err)
	sig[64] += 27
	prepared.Policy.Grant.OwnerSignature = sig

	require.NoError(t, StoreSessionPolicy(db, prepared.Policy))
	t.Logf("entities 1..%d are already spent on chain; allocating above them", highest)
}
