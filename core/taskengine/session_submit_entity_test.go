package taskengine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
)

func TestSubmitSessionPolicyRetriesSameSignatureAfterUnreadEntity(t *testing.T) {
	engine, _, key, owner, wallet := newPolicyTestEngine(t)
	var fail atomic.Bool
	var occupied atomic.Bool
	var calls atomic.Int32
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		calls.Add(1)
		reply := map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": "0x" + strings.Repeat("0", 64)}
		if occupied.Load() {
			reply["result"] = "0x" + strings.Repeat("0", 63) + "1"
		}
		if fail.Swap(false) {
			delete(reply, "result")
			reply["error"] = map[string]any{"code": -32000, "message": "occupancy RPC timed out"}
		}
		require.NoError(t, json.NewEncoder(w).Encode(reply))
	}))
	defer rpc.Close()
	engine.config.SmartWallet.EthRpcUrl = rpc.URL
	engine.sessionChainReads = true
	user := &model.User{Address: owner}
	in := SessionPolicyInput{Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "test", Permissions: testPermissions()}
	prepared, err := engine.PrepareSessionPolicy(user, in)
	require.NoError(t, err)
	signature := signDigest(t, key, prepared.Digest)
	calls.Store(0)
	fail.Store(true)
	_, _, err = engine.SubmitSessionPolicy(user, in, prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline, signature)
	require.ErrorIs(t, err, ErrSessionEntityUnverified)
	require.NotErrorIs(t, err, ErrSessionEntityTaken)
	require.EqualValues(t, 1, calls.Load(), "submit probes only the signed entity")
	policies, err := engine.ListSessionPoliciesForWallet(user, testPolicyChain, wallet)
	require.NoError(t, err)
	require.Empty(t, policies)
	occupied.Store(true)
	calls.Store(0)
	_, _, err = engine.SubmitSessionPolicy(user, in, prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline, signature)
	require.ErrorIs(t, err, ErrSessionEntityTaken)
	require.EqualValues(t, 1, calls.Load(), "occupied means refuse this entity, not allocate another")
	occupied.Store(false)
	stored, _, err := engine.SubmitSessionPolicy(user, in, prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline, signature)
	require.NoError(t, err)
	require.Equal(t, prepared.Policy.EntityID, stored.EntityID)
	require.Equal(t, signature, stored.Grant.OwnerSignature)
}

func TestSubmitEntityProbeHonorsDeadline(t *testing.T) {
	_, db, _, owner, wallet := newPolicyTestEngine(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	err := verifySessionEntityAvailable(ctx, db, testPolicyChain, owner, wallet, 1, func(ctx context.Context, _ common.Address, _ uint32) (bool, error) {
		_, bounded := ctx.Deadline()
		require.True(t, bounded)
		<-ctx.Done()
		return false, ctx.Err()
	})
	require.ErrorIs(t, err, ErrSessionEntityUnverified)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotErrorIs(t, err, ErrSessionEntityTaken)
}

func TestSubmitSessionPolicyKeepsPreparedEntityWhenEarlierProbeRecovers(t *testing.T) {
	engine, db, key, owner, wallet := newPolicyTestEngine(t)
	signer, err := engine.sessionSignerAddress()
	require.NoError(t, err)
	check := func(_ context.Context, _ common.Address, entity uint32) (bool, error) {
		if entity == 1 {
			return false, errors.New("temporary read failure")
		}
		return false, nil
	}
	perms := testPermissions()
	prepared, err := PrepareSessionGrant(db, testPolicyChain, signer, "01ARZ3NDEKTSV4RRFFQ69G5FAV", SessionGrantRequest{
		Owner: owner, Wallet: wallet, AgentLabel: "test", ValidUntil: perms.ValidUntilMs, HooksFor: perms.HooksFor, OccupancyCheck: check,
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, prepared.Policy.EntityID)
	stored, _, err := engine.SubmitSessionPolicy(&model.User{Address: owner}, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "test", Permissions: perms,
	}, prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline, signDigest(t, key, prepared.Digest))
	require.NoError(t, err)
	require.EqualValues(t, 2, stored.EntityID)
}
