package rest

import (
	"encoding/json"
	"math/big"
	"net/http"
	"sync"
	"testing"
	"time"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/common"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
)

func cleanupPolicyFixture(t *testing.T, rig *policyTestRig) *model.SessionPolicy {
	t.Helper()
	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	install, err := aa.PackSessionSignerInstall(aa.SessionGrant{
		EntityID: 7, Signer: signer, Global: true, Hooks: [][]byte{aa.AllowlistExecHook(7)},
	})
	require.NoError(t, err)
	return &model.SessionPolicy{
		ID: model.GenerateID(), Owner: &rig.owner, Runner: &rig.wallet,
		ChainID: policyTestChain, EntityID: 7, SessionSigner: &signer,
		AgentLabel: "Cleanup test", Status: model.SessionPolicyRevoked,
		ValidUntil: time.Now().Add(time.Hour).UnixMilli(), CreatedAt: time.Now().UnixMilli(),
		Grant: &model.SessionGrantAuthorization{
			InstallCall: install, AppliedAt: 1, CarrierNonce: big.NewInt(1),
			OwnerSignature: []byte("private fixture signature"),
		},
	}
}

func observePolicyCleanupWarnings(t *testing.T, server *Server) *observer.ObservedLogs {
	t.Helper()
	core, logs := observer.New(zap.WarnLevel)
	logger, err := sdklogging.NewZapLoggerByConfig(zap.NewDevelopmentConfig(), zap.WrapCore(func(zapcore.Core) zapcore.Core { return core }))
	require.NoError(t, err)
	server.logger = logger
	return logs
}

func readCleanupPolicy(t *testing.T, rig *policyTestRig, policyID string, list bool) map[string]json.RawMessage {
	t.Helper()
	address := rig.wallet.Hex()
	rec := rig.call(t, nil, func(c echo.Context) error {
		if list {
			return rig.server.ListWalletPolicies(c, address, generated.ListWalletPoliciesParams{})
		}
		return rig.server.GetWalletPolicy(c, address, policyID, generated.GetWalletPolicyParams{})
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	for _, marker := range []string{"installCall", "ownerSignature", "carrierNonce", "InstallCall"} {
		require.NotContains(t, rec.Body.String(), marker, "stored grant material must stay private")
	}
	var fields map[string]json.RawMessage
	if list {
		var result struct {
			Items []map[string]json.RawMessage `json:"items"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
		require.Len(t, result.Items, 1)
		fields = result.Items[0]
	} else {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &fields))
	}
	return fields
}

func assertCleanupFields(t *testing.T, fields map[string]json.RawMessage, required, payload bool) {
	t.Helper()
	require.Contains(t, fields, "onChainCleanupRequired", "false must be explicit too")
	var actual bool
	require.NoError(t, json.Unmarshal(fields["onChainCleanupRequired"], &actual))
	require.Equal(t, required, actual)
	if payload {
		require.True(t, actual, "never advertise a payload without required=true")
		require.Contains(t, fields, "onChainCleanup")
	} else {
		require.NotContains(t, fields, "onChainCleanup")
	}
}

func TestPolicyCleanupStateSurvivesListAndSingleReads(t *testing.T) {
	for _, tc := range []struct {
		name     string
		change   func(*model.SessionPolicy)
		required bool
		payload  bool
	}{
		{name: "applied revoked grant", required: true, payload: true},
		{name: "missing install", required: true, change: func(p *model.SessionPolicy) { p.Grant.InstallCall = nil }},
		{name: "corrupt install", required: true, change: func(p *model.SessionPolicy) { p.Grant.InstallCall = []byte{1, 2, 3, 4} }},
		{name: "torn down", change: func(p *model.SessionPolicy) { p.Grant.TornDownAt = 2 }},
		{name: "revoked never applied", change: func(p *model.SessionPolicy) { p.Grant.AppliedAt = 0 }},
		{name: "revoked without grant", change: func(p *model.SessionPolicy) { p.Grant = nil }},
		{name: "pending", change: func(p *model.SessionPolicy) { p.Status = model.SessionPolicyPending; p.Grant.AppliedAt = 0 }},
		{name: "active", change: func(p *model.SessionPolicy) { p.Status = model.SessionPolicyActive }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newPolicyRig(t)
			logs := observePolicyCleanupWarnings(t, rig.server)
			policy := cleanupPolicyFixture(t, rig)
			if tc.change != nil {
				tc.change(policy)
			}
			require.NoError(t, taskengine.StoreSessionPolicy(rig.db, policy))
			for _, list := range []bool{true, false, true, false} {
				fields := readCleanupPolicy(t, rig, policy.ID, list)
				assertCleanupFields(t, fields, tc.required, tc.payload)
				if tc.payload {
					var cleanup generated.OnChainRevokeCleanup
					require.NoError(t, json.Unmarshal(fields["onChainCleanup"], &cleanup))
					require.Equal(t, rig.wallet.Hex(), cleanup.Target)
					require.EqualValues(t, policy.ChainID, cleanup.ChainId)
					require.EqualValues(t, policy.EntityID, cleanup.EntityId)
					want, err := aa.SessionSignerUninstallFromInstall(policy.EntityID, policy.Grant.InstallCall)
					require.NoError(t, err)
					require.Equal(t, common.Bytes2Hex(want), cleanup.CallData[2:])
				}
			}
			if tc.required && !tc.payload {
				require.Equal(t, 1, logs.Len(), "polling list/detail must not flood the log")
				entry := logs.All()[0]
				tags := entry.ContextMap()
				require.Equal(t, policy.ID, tags["policy_id"])
				require.Equal(t, policy.ChainID, tags["chain_id"])
				require.Equal(t, rig.wallet.Hex(), tags["wallet"])
				_, cause := taskengine.BuildOnChainRevokeCleanup(policy)
				require.Error(t, cause)
				require.Equal(t, cause.Error(), tags["error"])
			} else {
				require.Zero(t, logs.Len())
			}
		})
	}
}

func TestPolicyCleanupRequiredAfterFailedRevokeAndPayloadRecovery(t *testing.T) {
	rig := newPolicyRig(t)
	policy := cleanupPolicyFixture(t, rig)
	install := policy.Grant.InstallCall
	policy.Status = model.SessionPolicyActive
	policy.Grant.InstallCall = []byte{1} // Persisted corruption; cleanup cannot be reconstructed.
	require.NoError(t, taskengine.StoreSessionPolicy(rig.db, policy))
	rec := rig.call(t, nil, func(c echo.Context) error {
		return rig.server.RevokeWalletPolicy(c, rig.wallet.Hex(), policy.ID, generated.RevokeWalletPolicyParams{})
	}, nil)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Contains(t, rec.Body.String(), "POLICIES_REVOKED_CLEANUP_UNAVAILABLE")

	// A new HTTP server has no modal/request state to remember the revoke.
	rig.server = &Server{engine: rig.server.engine, config: rig.server.config}
	logs := observePolicyCleanupWarnings(t, rig.server)
	for _, list := range []bool{true, false} {
		assertCleanupFields(t, readCleanupPolicy(t, rig, policy.ID, list), true, false)
	}
	require.Equal(t, 1, logs.Len())

	stored, err := rig.server.engine.GetSessionPolicyByID(&model.User{Address: rig.owner}, policyTestChain, rig.wallet, policy.ID)
	require.NoError(t, err)
	require.Equal(t, model.SessionPolicyRevoked, stored.Status)
	stored.Grant.InstallCall = install
	require.NoError(t, taskengine.StoreSessionPolicy(rig.db, stored))
	for _, list := range []bool{true, false} {
		assertCleanupFields(t, readCleanupPolicy(t, rig, policy.ID, list), true, true)
	}

	// Only a recorded teardown clears the requirement, never a payload build.
	stored.Grant.TornDownAt = time.Now().UnixMilli()
	require.NoError(t, taskengine.StoreSessionPolicy(rig.db, stored))
	for _, list := range []bool{true, false} {
		assertCleanupFields(t, readCleanupPolicy(t, rig, policy.ID, list), false, false)
	}
	require.Equal(t, 1, logs.Len())
}

func TestPolicyCleanupWarningsAreConcurrentAndPerPolicy(t *testing.T) {
	rig := newPolicyRig(t)
	logs := observePolicyCleanupWarnings(t, rig.server)
	policy := cleanupPolicyFixture(t, rig)
	policy.Grant.InstallCall = nil
	const readers = 32
	results := make(chan generated.SessionPolicy, readers)
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- rig.server.policyToAPI(policy)
		}()
	}
	wg.Wait()
	close(results)
	for result := range results {
		require.True(t, result.OnChainCleanupRequired)
		require.Nil(t, result.OnChainCleanup)
	}
	require.Equal(t, 1, logs.Len())

	other := *policy
	other.ID = model.GenerateID()
	rig.server.policyToAPI(&other)
	require.Equal(t, 2, logs.Len(), "one corrupt policy must not hide another's failure")
}

func TestSubmitPolicyResponseRetainsCleanupRequirement(t *testing.T) {
	rig := newPolicyRig(t)
	policy := cleanupPolicyFixture(t, rig)
	for _, payload := range []bool{true, false} {
		if !payload {
			policy.Grant.InstallCall = nil
		}
		body, err := json.Marshal(rig.server.submitPolicyToAPI(policy, nil))
		require.NoError(t, err)
		var fields map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(body, &fields))
		assertCleanupFields(t, fields, true, payload)
	}
}

func TestRevokePolicyCleanupResponseIsUnchanged(t *testing.T) {
	for _, state := range []string{"applied", "pending", "torn down"} {
		t.Run(state, func(t *testing.T) {
			rig := newPolicyRig(t)
			policy := cleanupPolicyFixture(t, rig)
			policy.Status = model.SessionPolicyActive
			switch state {
			case "pending":
				policy.Status = model.SessionPolicyPending
				policy.Grant.AppliedAt = 0
			case "torn down":
				policy.Grant.TornDownAt = 2
			}
			require.NoError(t, taskengine.StoreSessionPolicy(rig.db, policy))
			rec := rig.call(t, nil, func(c echo.Context) error {
				return rig.server.RevokeWalletPolicy(c, rig.wallet.Hex(), policy.ID, generated.RevokeWalletPolicyParams{})
			}, nil)
			require.Equal(t, http.StatusOK, rec.Code)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &fields))
			require.JSONEq(t, `"revoked"`, string(fields["status"]))
			assertCleanupFields(t, fields, state == "applied", state == "applied")
			expectedFields := 2
			if state == "applied" {
				expectedFields++
			}
			require.Len(t, fields, expectedFields, "revoke keeps its existing response shape")
		})
	}
}
