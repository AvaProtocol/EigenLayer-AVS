package taskengine

import (
	"context"
	"errors"
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

// storeCoveringGrantSignedBy is storeCoveringGrant with the installed signer
// set, so a test can name the gateway's current signer.
func storeCoveringGrantSignedBy(t *testing.T, db storage.Storage, id string, owner, runner common.Address, until int64, signer common.Address) {
	t.Helper()
	storeCoveringGrant(t, db, id, owner, runner, until)
	policy, err := ActiveSessionPolicyForWallet(db, skillSepolia, owner, runner)
	if err != nil || policy == nil || policy.ID != id {
		t.Fatalf("reading back %s = %#v, %v", id, policy, err)
	}
	policy.SessionSigner = &signer
	if err := StoreSessionPolicy(db, policy); err != nil {
		t.Fatal(err)
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
