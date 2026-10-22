package taskengine

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// retiredSignerEngine is a deploy-check engine whose session signer is a fresh
// key, so every grant storeCoveringGrant writes names a retired signer.
func retiredSignerEngine(t *testing.T, db storage.Storage) (*Engine, common.Address) {
	t.Helper()
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	engine := deployCheckEngine(db)
	engine.config.SessionSignerPrivateKey = key
	return engine, crypto.PubkeyToAddress(key.PublicKey)
}

// storeChainGrantSignedBy is storeChainGrant with the installed signer set,
// so a test can name the gateway's current signer.
func storeChainGrantSignedBy(t *testing.T, db storage.Storage, id string, owner, runner common.Address, chain, until int64, actions []model.AllowedAction, signer common.Address) {
	t.Helper()
	storeChainGrant(t, db, id, owner, runner, chain, until, actions)
	policy, err := ActiveSessionPolicyForWallet(db, chain, owner, runner)
	if err != nil || policy == nil || policy.ID != id {
		t.Fatalf("reading back %s = %#v, %v", id, policy, err)
	}
	policy.SessionSigner = &signer
	if err := StoreSessionPolicy(db, policy); err != nil {
		t.Fatal(err)
	}
}

func storeCoveringGrantSignedBy(t *testing.T, db storage.Storage, id string, owner, runner common.Address, until int64, signer common.Address) {
	t.Helper()
	storeChainGrantSignedBy(t, db, id, owner, runner, skillSepolia, until, []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)}, signer)
}

// An unreadable target outranks a grant-again refusal on another chain even
// when a third chain also has an ordinary coverage gap: granting again cannot
// make the workflow deployable while no grant can name that target. Holds for
// an expired grant and a retired-signer grant alike.
func TestDeployCheckUnresolvedTargetOutranksGrantAgainAlongsideOrdinaryGap(t *testing.T) {
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })
	now := time.Now()
	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
	const grantAgainChain int64 = 10

	cases := []struct {
		name            string
		owner, runner   string
		retired         bool
		grantAgainUntil int64
	}{
		{"expired grant", "0x0000000000000000000000000000000000000051", "0x0000000000000000000000000000000000000052", false, now.Add(-time.Hour).UnixMilli()},
		{"retired-signer grant", "0x0000000000000000000000000000000000000053", "0x0000000000000000000000000000000000000054", true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			on := deployCheckEngine(db)
			// Without a configured signer nothing is retired, so the
			// expired case keeps every grant usable apart from its expiry.
			current := common.HexToAddress(skillUSDC)
			if tc.retired {
				on, current = retiredSignerEngine(t, db)
			}
			owner := common.HexToAddress(tc.owner)
			runner := common.HexToAddress(tc.runner)
			live := now.Add(24 * time.Hour).UnixMilli()

			// Chain 1: an ordinary gap (approve granted, transfer needed).
			task := skillWriteTask("pay", "Pay", skillWETH, transferCalldata(payee, big.NewInt(1)), 1, 1, 0, 0)
			storeChainGrantSignedBy(t, db, "01ordinarygap"+tc.owner[38:], owner, runner, 1, live, []model.AllowedAction{skillAction(skillWETH, selectorApprove)}, current)
			// Chain 10: a covering grant the owner must grant again.
			again := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(1)), grantAgainChain, 1, 0, 0)
			task.Nodes = append(task.Nodes, again.Nodes...)
			storeChainGrant(t, db, "01grantagain"+tc.owner[38:], owner, runner, grantAgainChain, tc.grantAgainUntil, []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)})
			// Sepolia: a live grant and a target no grant can name.
			unresolved := skillWriteTask("pay", "Pay", "{{settings.token}}", transferCalldata(payee, big.NewInt(1)), skillSepolia, 1, 0, 0)
			task.Nodes = append(task.Nodes, unresolved.Nodes...)
			unresolvedID := "01unresolvedtarget" + tc.owner[38:]
			storeChainGrantSignedBy(t, db, unresolvedID, owner, runner, skillSepolia, live, []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)}, current)

			_, err := on.enforceSessionPolicyDeployCheck(&model.User{Address: owner}, skillWorkflow(task, runner))
			var conflict *PolicyConflictError
			if !errors.As(err, &conflict) || conflict.Code != SessionPolicyTargetUnresolvedCode || conflict.PolicyID != unresolvedID {
				t.Fatalf("three-chain deploy = %#v", err)
			}
			if conflict.Required == nil || conflict.Required.ChainID != skillSepolia || conflict.Missing != nil {
				t.Fatalf("required and missing must follow the unreadable target: %#v", conflict)
			}
			if strings.Contains(conflict.Detail, "grant again") {
				t.Fatalf("detail = %q", conflict.Detail)
			}
			for _, mu := range orderedSessionLocks([]int64{1, grantAgainChain, skillSepolia}, owner, runner) {
				if !mu.TryLock() {
					t.Fatal("a refusal must release the runner lock")
				}
				mu.Unlock()
			}
		})
	}
}

// A grant that covers the workflow but names a signer the gateway no longer
// holds can never execute, so create and resume refuse it with the stable
// "grant again" code instead of enabling an automation that cannot run.
func TestDeployCheckRefusesGrantForRetiredSigner(t *testing.T) {
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })
	on, current := retiredSignerEngine(t, db)
	now := time.Now()

	t.Run("covering grant for a retired signer is refused", func(t *testing.T) {
		owner := common.HexToAddress("0x0000000000000000000000000000000000000031")
		runner := common.HexToAddress("0x0000000000000000000000000000000000000032")
		storeCoveringGrant(t, db, "01retiredsigner00000000000", owner, runner, 0)
		_, err := on.enforceSessionPolicyDeployCheck(&model.User{Address: owner}, skillWorkflow(skillManualTransfer("pay", 0), runner))
		var conflict *PolicyConflictError
		if !errors.As(err, &conflict) || conflict.Code != SessionPolicySignerRetiredCode {
			t.Fatalf("retired signer deploy = %#v", err)
		}
		if !errors.Is(err, ErrSessionPolicyNotCovering) {
			t.Fatal("the refusal must keep the coverage sentinel REST maps to 409")
		}
		if conflict.PolicyID != "01retiredsigner00000000000" || len(conflict.AffectedTaskIDs) != 1 || conflict.AffectedTaskIDs[0] != "pay" {
			t.Fatalf("conflict = %#v", conflict)
		}
		if conflict.Required == nil || !conflict.Required.HasFundMove || conflict.Missing != nil {
			t.Fatalf("a client grants again from required: %#v", conflict)
		}
		if strings.Contains(conflict.Detail, ":") || !strings.Contains(conflict.Detail, "grant again") {
			t.Fatalf("detail must be colon-free and actionable: %q", conflict.Detail)
		}
		mu := sessionAuthorityLock(skillSepolia, owner, runner)
		if !mu.TryLock() {
			t.Fatal("a refusal must release the runner lock")
		}
		mu.Unlock()
	})

	t.Run("grant for the current signer deploys", func(t *testing.T) {
		owner := common.HexToAddress("0x0000000000000000000000000000000000000033")
		runner := common.HexToAddress("0x0000000000000000000000000000000000000034")
		storeCoveringGrantSignedBy(t, db, "01currentsigner00000000000", owner, runner, 0, current)
		unlock, err := on.enforceSessionPolicyDeployCheck(&model.User{Address: owner}, skillWorkflow(skillManualTransfer("pay", 0), runner))
		if err != nil {
			t.Fatal(err)
		}
		unlock()
	})

	t.Run("expiry outranks a retired signer", func(t *testing.T) {
		owner := common.HexToAddress("0x0000000000000000000000000000000000000035")
		runner := common.HexToAddress("0x0000000000000000000000000000000000000036")
		storeCoveringGrant(t, db, "01retiredexpired0000000000", owner, runner, now.Add(-time.Hour).UnixMilli())
		_, err := on.enforceSessionPolicyDeployCheck(&model.User{Address: owner}, skillWorkflow(skillManualTransfer("pay", 0), runner))
		var conflict *PolicyConflictError
		if !errors.As(err, &conflict) || conflict.Code != SessionPolicyExpiredCode {
			t.Fatalf("expired and retired = %#v", err)
		}
	})

	t.Run("notification still deploys on a retired grant", func(t *testing.T) {
		owner := common.HexToAddress("0x0000000000000000000000000000000000000037")
		runner := common.HexToAddress("0x0000000000000000000000000000000000000038")
		storeCoveringGrant(t, db, "01retirednote0000000000000", owner, runner, 0)
		note := skillWorkflow(&avsproto.Task{Id: "note", Name: "Ping"}, runner)
		unlock, err := on.enforceSessionPolicyDeployCheck(&model.User{Address: owner}, note)
		if err != nil {
			t.Fatal(err)
		}
		unlock()
	})
}

// Readiness (the simulate verdict's authorization) agrees with deploy: a
// retired-signer grant is no usable grant, which sends the client to its grant
// step with the same requirement.
func TestSimulateAuthorizationReportsRetiredSignerAsNoGrant(t *testing.T) {
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })
	on, current := retiredSignerEngine(t, db)

	verdict := func(owner, runner common.Address) *SessionAuthorization {
		// One run makes the transfer's total spend a fixed number, so the
		// current-signer control is a plain "covered".
		auth := &SimulateAuth{Report: true, MaxExecution: 1}
		vm := &VM{db: db, mu: &sync.Mutex{}, vars: map[string]any{"aa_sender": runner.Hex()}}
		on.fillSimulateAuthorization(WithSimulateAuth(context.Background(), auth), &model.User{Address: owner},
			skillWorkflow(skillManualTransfer("pay", 0), runner), vm, skillSepolia)
		if auth.Result == nil {
			t.Fatal("no authorization verdict")
		}
		return auth.Result
	}

	owner := common.HexToAddress("0x0000000000000000000000000000000000000041")
	runner := common.HexToAddress("0x0000000000000000000000000000000000000042")
	storeCoveringGrant(t, db, "01retiredverdict0000000000", owner, runner, 0)
	got := verdict(owner, runner)
	if got.Status != AuthNoGrant || got.PolicyID != "01retiredverdict0000000000" || got.Required == nil {
		t.Fatalf("retired signer verdict = %#v", got)
	}
	if !strings.Contains(got.Detail, "grant again") {
		t.Fatalf("detail = %q", got.Detail)
	}

	owner = common.HexToAddress("0x0000000000000000000000000000000000000043")
	runner = common.HexToAddress("0x0000000000000000000000000000000000000044")
	storeCoveringGrantSignedBy(t, db, "01currentverdict0000000000", owner, runner, 0, current)
	if got := verdict(owner, runner); got.Status != AuthCovered {
		t.Fatalf("current signer verdict = %#v", got)
	}
}

// The resolver refusal is machine-readable: it carries the stable code, so
// execution errors and the bundler classification name the remedy.
func TestResolverRefusesRetiredSignerWithStableCode(t *testing.T) {
	engine, _, _, _, _ := newPolicyTestEngine(t)
	retired := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	_, err := configuredSessionSigner(engine.config)(retired)
	if err == nil || !strings.HasPrefix(err.Error(), SessionPolicySignerRetiredCode+": ") {
		t.Fatalf("retired signer = %v", err)
	}
	if !engine.sessionSignerRetired(&model.SessionPolicy{SessionSigner: &retired}) {
		t.Fatal("a grant naming another signer is retired")
	}
	current, err := engine.sessionSignerAddress()
	if err != nil {
		t.Fatal(err)
	}
	if engine.sessionSignerRetired(&model.SessionPolicy{SessionSigner: &current}) {
		t.Fatal("a grant naming the configured signer is not retired")
	}
	if !engine.sessionSignerRetired(&model.SessionPolicy{}) {
		t.Fatal("a grant with no signer cannot be signed for")
	}
	if (&Engine{}).sessionSignerRetired(&model.SessionPolicy{SessionSigner: &retired}) {
		t.Fatal("with no session signer configured nothing is called retired")
	}
}
