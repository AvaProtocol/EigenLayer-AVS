package rest

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/generated"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/labstack/echo/v4"
)

type cleanupRegistrationProbe struct {
	calls        atomic.Int32
	registered   atomic.Bool
	unavailable  atomic.Bool
	malformed    atomic.Bool
	wrongRequest atomic.Bool
}

func newCleanupRegistrationProbe(t *testing.T, rig *policyTestRig) *cleanupRegistrationProbe {
	t.Helper()
	view, err := abi.JSON(strings.NewReader(`[{"type":"function","name":"getValidationData","stateMutability":"view","inputs":[{"name":"validationFunction","type":"bytes24"}],"outputs":[{"name":"data","type":"tuple","components":[{"name":"validationFlags","type":"uint8"},{"name":"validationHooks","type":"bytes25[]"},{"name":"executionHooks","type":"bytes25[]"},{"name":"selectors","type":"bytes4[]"}]}]}]`))
	require.NoError(t, err)
	type registration struct {
		ValidationFlags uint8
		ValidationHooks [][25]byte
		ExecutionHooks  [][25]byte
		Selectors       [][4]byte
	}
	live, err := view.Methods["getValidationData"].Outputs.Pack(registration{ValidationFlags: 1})
	require.NoError(t, err)
	cleared, err := view.Methods["getValidationData"].Outputs.Pack(registration{})
	require.NoError(t, err)
	input, err := view.Pack("getValidationData", aa.PackModuleEntity(aa.SingleSignerValidationModuleAddress(), 7))
	require.NoError(t, err)
	probe := &cleanupRegistrationProbe{}
	probe.registered.Store(true)
	rpc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probe.calls.Add(1)
		var req struct {
			ID     json.RawMessage   `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.Method != "eth_call" || len(req.Params) != 2 {
			probe.wrongRequest.Store(true)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var call map[string]string
		var block string
		if json.Unmarshal(req.Params[0], &call) != nil || json.Unmarshal(req.Params[1], &block) != nil {
			probe.wrongRequest.Store(true)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		data := call["input"]
		if data == "" {
			data = call["data"]
		}
		if common.HexToAddress(call["to"]) != rig.wallet || data != "0x"+common.Bytes2Hex(input) || block != "latest" {
			probe.wrongRequest.Store(true)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if probe.unavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		result := "0x" + common.Bytes2Hex(cleared)
		if probe.registered.Load() {
			result = "0x" + common.Bytes2Hex(live)
		}
		if probe.malformed.Load() {
			result = "0x"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(rpc.Close)
	rig.server.config.SmartWallet.EthRpcUrl = rpc.URL
	rig.server.engine.InstallSessionResolver()
	t.Cleanup(func() {
		preset.SetSessionResolver(nil)
		preset.SetSessionSendGuard(nil)
		preset.SetEOA7702AccountLookup(nil)
	})
	return probe
}

func TestPolicyCleanupReadReconcilesOwnerRemoval(t *testing.T) {
	for _, list := range []bool{true, false} {
		for _, missingPayload := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{true: "list", false: "single"}[list], map[bool]string{true: "missing payload", false: "payload"}[missingPayload]}, "/"), func(t *testing.T) {
				rig := newPolicyRig(t)
				policy := cleanupPolicyFixture(t, rig)
				if missingPayload {
					policy.Grant.InstallCall = nil
				}
				require.NoError(t, taskengine.StoreSessionPolicy(rig.db, policy))
				probe := newCleanupRegistrationProbe(t, rig)
				assertCleanupFields(t, readCleanupPolicy(t, rig, policy.ID, list), true, !missingPayload)
				require.EqualValues(t, 1, probe.calls.Load())

				// The owner removes the entity outside the gateway. No client
				// confirmation or subsequent grant is needed to clear the read.
				probe.registered.Store(false)
				assertCleanupFields(t, readCleanupPolicy(t, rig, policy.ID, list), false, false)
				require.EqualValues(t, 2, probe.calls.Load())
				stored, err := rig.server.engine.GetSessionPolicyByID(&model.User{Address: rig.owner}, policyTestChain, rig.wallet, policy.ID)
				require.NoError(t, err)
				require.True(t, stored.Grant.TornDown())

				// A different server/browser sees the persisted result and
				// neither policy endpoint makes another registration read.
				rig.server = &Server{engine: rig.server.engine, config: rig.server.config, logger: rig.server.logger}
				for _, nextList := range []bool{true, false} {
					assertCleanupFields(t, readCleanupPolicy(t, rig, policy.ID, nextList), false, false)
				}
				require.EqualValues(t, 2, probe.calls.Load())
				require.False(t, probe.wrongRequest.Load())
			})
		}
	}
}

func TestPolicyCleanupReadFailureKeepsRequirement(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(map[bool]string{false: "RPC failure", true: "malformed reply"}[malformed], func(t *testing.T) {
			rig := newPolicyRig(t)
			policy := cleanupPolicyFixture(t, rig)
			require.NoError(t, taskengine.StoreSessionPolicy(rig.db, policy))
			probe := newCleanupRegistrationProbe(t, rig)
			probe.registered.Store(false)
			probe.unavailable.Store(!malformed)
			probe.malformed.Store(malformed)
			logs := observePolicyCleanupWarnings(t, rig.server)
			for _, list := range []bool{true, false, true} {
				assertCleanupFields(t, readCleanupPolicy(t, rig, policy.ID, list), true, true)
			}
			stored, err := rig.server.engine.GetSessionPolicyByID(&model.User{Address: rig.owner}, policyTestChain, rig.wallet, policy.ID)
			require.NoError(t, err)
			require.False(t, stored.Grant.TornDown())
			require.Equal(t, 1, logs.Len(), "failed verification is rate limited independently of payload failures")
			require.Equal(t, "verification", logs.All()[0].ContextMap()["stage"])
			require.EqualValues(t, 3, probe.calls.Load())
			require.False(t, probe.wrongRequest.Load())
		})
	}
}

type cleanupWriteFailure struct {
	storage.Storage
	key  []byte
	fail atomic.Bool
}

func (db *cleanupWriteFailure) Set(key, value []byte) error {
	if bytes.Equal(key, db.key) && db.fail.Load() {
		return errors.New("cleanup marker storage unavailable")
	}
	return db.Storage.Set(key, value)
}

func TestPolicyCleanupReadStorageFailureKeepsRequirement(t *testing.T) {
	rig := newPolicyRig(t)
	policy := cleanupPolicyFixture(t, rig)
	require.NoError(t, taskengine.StoreSessionPolicy(rig.db, policy))
	db := &cleanupWriteFailure{Storage: rig.db, key: taskengine.SessionPolicyKey(policyTestChain, rig.owner, policy.ID)}
	db.fail.Store(true)
	rig.server.engine = taskengine.New(db, rig.server.config, nil, rig.server.logger)
	probe := newCleanupRegistrationProbe(t, rig)
	probe.registered.Store(false)
	assertCleanupFields(t, readCleanupPolicy(t, rig, policy.ID, true), true, true)

	db.fail.Store(false)
	assertCleanupFields(t, readCleanupPolicy(t, rig, policy.ID, false), false, false)
	require.EqualValues(t, 2, probe.calls.Load())
}

func TestPolicyCleanupReadUsesPolicyChain(t *testing.T) {
	rig := newPolicyRig(t)
	policy := cleanupPolicyFixture(t, rig)
	policy.ChainID = policyGrantChain
	require.NoError(t, taskengine.StoreSessionPolicy(rig.db, policy))
	probe := newCleanupRegistrationProbe(t, rig)
	probe.registered.Store(false)
	rig.server.config.Chains[1].SmartWallet.EthRpcUrl = rig.server.config.SmartWallet.EthRpcUrl
	rig.server.config.SmartWallet.EthRpcUrl = "http://127.0.0.1:1"
	rec := rig.callAs(t, policyGrantChain, nil, func(c echo.Context) error {
		return rig.server.GetWalletPolicy(c, rig.wallet.Hex(), policy.ID, generated.GetWalletPolicyParams{})
	}, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &fields))
	assertCleanupFields(t, fields, false, false)
	require.EqualValues(t, 1, probe.calls.Load())
	require.False(t, probe.wrongRequest.Load())
}

func TestPolicyCleanupReadsSkipPoliciesWithoutCleanup(t *testing.T) {
	for _, state := range []string{"active", "pending", "unapplied", "torn down", "no grant"} {
		t.Run(state, func(t *testing.T) {
			rig := newPolicyRig(t)
			policy := cleanupPolicyFixture(t, rig)
			switch state {
			case "active":
				policy.Status = model.SessionPolicyActive
			case "pending":
				policy.Status = model.SessionPolicyPending
				policy.Grant.AppliedAt = 0
			case "unapplied":
				policy.Grant.AppliedAt = 0
			case "torn down":
				policy.Grant.TornDownAt = 1
			case "no grant":
				policy.Grant = nil
			}
			require.NoError(t, taskengine.StoreSessionPolicy(rig.db, policy))
			probe := newCleanupRegistrationProbe(t, rig)
			for _, list := range []bool{true, false} {
				assertCleanupFields(t, readCleanupPolicy(t, rig, policy.ID, list), false, false)
			}
			require.Zero(t, probe.calls.Load())
		})
	}
}
