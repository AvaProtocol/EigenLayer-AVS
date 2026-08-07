//go:build integration
// +build integration

package taskengine

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
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// The two things #731 shipped that had never run on a chain.
//
// TestGrantReplaceClearsThePriorEntity_Sepolia covers one prior entity. #731
// generalised that to N and added a chain read that drops candidates already
// clear, and neither generalisation had live coverage — the existing test
// asserts exactly one supersede, so it passes unchanged either way.
//
// Both are the kind of thing offline tests cannot see. An uninstall whose
// hookUninstallData is misrouted MINES with success=true and clears nothing,
// and an uninstall aimed at an entity that is already clear REVERTS, taking
// the whole deferred batch with it — so the new grant never installs. That
// second one is why dropClearedTeardownTargets exists: storage believes an
// entity is installed long after the owner cleared it by hand through
// onChainCleanup, because nothing tells the gateway it happened.
//
//	OWNER_EOA, TEST_PRIVATE_KEY, config/test.yaml.
//	go test -tags=integration ./core/taskengine -run TestGrantReplaceClearsSeveralPriorEntities_Sepolia -v
func TestGrantReplaceClearsSeveralPriorEntities_Sepolia(t *testing.T) {
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

	// Four operations, each self-funded — the fixture is not on the Gas
	// Manager policy and pointing a test at it would have production pay.
	requireFundedRunner(t, cfg.SmartWallet, *runner, big.NewInt(12_000_000_000_000_000)) // 0.012 ETH

	code, err := client.CodeAt(context.Background(), *runner, nil)
	require.NoError(t, err)
	require.NotEmpty(t, code,
		"runner %s is not deployed; deploy it with scripts/fixture_wallet -salt %d -deploy — an undeployed account reads every entity as clear",
		runner.Hex(), fixtureSaltGrantReplace)

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

	// ── 1. Build a wallet carrying TWO installed entities.
	//
	// Every install here rides a real operation, so both InstallCalls are the
	// bytes the owner actually signed — teardown is derived from those, and a
	// reconstructed payload would mine and clear nothing.
	//
	// Marking the first torn down between the grants is what keeps the second
	// from replacing it. That is exactly the shape of a wallet that stacked
	// before on-chain replace shipped, which is the case #717 AC1 names.
	first := grantThroughEngine(t, engine, user, ownerKey, *runner)
	runOperationUnderGrant(t, engine, user, *runner)
	requireEntityClear(t, client, *runner, first.EntityID, false)
	require.NoError(t, MarkEntityTornDown(db, chainID.Int64(), owner, *runner, first.EntityID))

	second := grantThroughEngine(t, engine, user, ownerKey, *runner)
	require.NotEqual(t, first.EntityID, second.EntityID)
	runOperationUnderGrant(t, engine, user, *runner)
	requireEntityClear(t, client, *runner, second.EntityID, false)

	// Un-hide the first: storage now agrees with the chain that both are live.
	clearTornDownMark(t, db, chainID.Int64(), owner, *runner, first.EntityID)
	t.Logf("stacked: entities %d and %d both installed", first.EntityID, second.EntityID)

	// ── 2. One grant, N-way teardown. This is the part that has never run.
	prepared := firstPrepared(t, engine, user)
	require.Len(t, prepared.Supersedes, 2,
		"both installed entities must ride this batch; one-at-a-time is what #717 AC1 rejects")
	require.ElementsMatch(t,
		[]uint32{first.EntityID, second.EntityID},
		[]uint32{prepared.Supersedes[0].EntityID, prepared.Supersedes[1].EntityID})

	third := submitPrepared(t, engine, user, ownerKey, prepared)
	runOperationUnderGrant(t, engine, user, *runner)

	// A mined batch is not evidence. Read the modules back.
	requireEntityClear(t, client, *runner, first.EntityID, true)
	requireEntityClear(t, client, *runner, second.EntityID, true)
	requireEntityClear(t, client, *runner, third.EntityID, false)
	t.Logf("✅ entities %d and %d cleared in one batch; %d holds the replacement",
		first.EntityID, second.EntityID, third.EntityID)

	usable := usableOn(t, db, owner, *runner)
	require.Len(t, usable, 1, "a replace leaves exactly one usable grant")
	require.Equal(t, third.ID, usable[0].ID)

	// ── 3. An entity that is already clear must be DROPPED, not batched.
	//
	// Clearing the mark reproduces the state after an owner runs the
	// onChainCleanup payload in their own wallet: the entity is gone from the
	// chain and storage never found out. Batching its uninstall reverts the
	// whole deferred action, so the next grant would fail — and keep failing,
	// because only a successful replace marks TornDownAt.
	clearTornDownMark(t, db, chainID.Int64(), owner, *runner, first.EntityID)

	afterCleanup := firstPrepared(t, engine, user)
	for _, s := range afterCleanup.Supersedes {
		require.NotEqual(t, first.EntityID, s.EntityID,
			"entity %d is already clear on chain; batching its uninstall reverts the whole grant",
			first.EntityID)
	}
	require.True(t, tornDownOnRecord(t, db, chainID.Int64(), owner, *runner, first.EntityID),
		"a candidate found clear on chain must be recorded torn down, or every later grant re-reads it")

	fourth := submitPrepared(t, engine, user, ownerKey, afterCleanup)
	runOperationUnderGrant(t, engine, user, *runner)
	requireEntityClear(t, client, *runner, fourth.EntityID, false)
	t.Logf("✅ stale candidate %d dropped; grant %d still landed", first.EntityID, fourth.EntityID)
}

// clearTornDownMark removes the TornDownAt marker from whichever policy holds
// entity on runner, putting storage back to believing the entity is installed.
//
// Used to stage two situations that arise for real but cannot be produced
// through the API: a wallet that stacked before on-chain replace shipped, and
// one whose owner ran the explicit onChainCleanup payload without the gateway
// ever learning it happened.
func clearTornDownMark(t *testing.T, db storage.Storage, chainID int64, owner, runner common.Address, entity uint32) {
	t.Helper()
	policies, err := ListSessionPolicies(db, chainID, owner)
	require.NoError(t, err)
	for _, p := range policies {
		if p.Runner == nil || *p.Runner != runner || p.EntityID != entity || p.Grant == nil {
			continue
		}
		p.Grant.TornDownAt = 0
		require.NoError(t, StoreSessionPolicy(db, p))
		return
	}
	t.Fatalf("no stored policy holds entity %d on %s", entity, runner.Hex())
}

func tornDownOnRecord(t *testing.T, db storage.Storage, chainID int64, owner, runner common.Address, entity uint32) bool {
	t.Helper()
	policies, err := ListSessionPolicies(db, chainID, owner)
	require.NoError(t, err)
	for _, p := range policies {
		if p.Runner == nil || *p.Runner != runner || p.EntityID != entity || p.Grant == nil {
			continue
		}
		return p.Grant.TornDown()
	}
	t.Fatalf("no stored policy holds entity %d on %s", entity, runner.Hex())
	return false
}
