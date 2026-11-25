package taskengine

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/AvaProtocol/EigenLayer-AVS/aggregator/rest/mapping"
	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/erc4337/preset"
	"github.com/AvaProtocol/EigenLayer-AVS/pkg/logger"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
)

// Serve the production allowance and fee reads. A nil token remainder
// represents an unreadable allowance, distinct from a successful read of zero.
type sweepBudgetRPC struct {
	tokens map[common.Address]*big.Int
	native *big.Int
}

func (s *sweepBudgetRPC) Call(call struct {
	To    common.Address
	Input hexutil.Bytes
}, _ string) (hexutil.Bytes, error) {
	var left *big.Int
	switch {
	case call.To == aa.AllowlistModuleAddress() && len(call.Input) == 100:
		left = s.tokens[common.BytesToAddress(call.Input[36:68])]
	case call.To == aa.NativeTokenLimitModuleAddress():
		left = s.native
	}
	if left == nil {
		return nil, errors.New("allowance unavailable")
	}
	return common.LeftPadBytes(left.Bytes(), 32), nil
}
func (*sweepBudgetRPC) MaxPriorityFeePerGas() hexutil.Uint64 { return 1 }
func (*sweepBudgetRPC) GetBlockByNumber(string, bool) *types.Header {
	return &types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(0), BaseFee: big.NewInt(0)}
}

// Compare the JSON the SDK reads with the gateway's report payload.
func publishedLoopFailures(t *testing.T, vm *VM, id string) []any {
	t.Helper()
	wire, err := mapping.ProtoToOpenAPIExecution(&avsproto.Execution{Steps: vm.ExecutionLogs}, "sweep")
	require.NoError(t, err)
	encoded, err := json.Marshal(wire)
	require.NoError(t, err)
	var rest struct {
		Steps []struct {
			ID       string
			Metadata map[string]any
			Output   map[string]any
		}
	}
	require.NoError(t, json.Unmarshal(encoded, &rest))
	report, err := NewContextMemorySummarizer("", "").BuildNotifyPayload(vm, "report")
	require.NoError(t, err)
	for i, step := range rest.Steps {
		if step.ID == id {
			reportStep := report["steps"].([]any)[i].(map[string]any)
			require.Equal(t, id, reportStep["id"])
			require.Equal(t, step.Metadata, reportStep["metadata"])
			require.Equal(t, dependencyStep(t, vm, id).GetLoop().GetData().AsInterface(), step.Output["data"])
			require.Equal(t, step.Output["data"], reportStep["outputData"])
			return step.Metadata["iterationFailures"].([]any)
		}
	}
	t.Fatalf("missing loop %q", id)
	return nil
}

// Execute the compiled planner and both loops, with fixture balances in
// place of the balance provider. The selected zero-balance row has no index.
func newSweepTestVM(t *testing.T, db storage.Storage, rpcURL string, tokens []common.Address, amount string) *VM {
	t.Helper()
	selections := []any{tokens[0].Hex(), common.HexToAddress("0x44").Hex(), tokens[1].Hex(), tokens[2].Hex(), budgetNativeToken.Hex()}
	settings := sweepSettings(selections...)
	settings["name"], settings["runner"], settings["chain_id"] = "Sweep", spWallet.Hex(), skillSepolia
	task := sweepTask(t, sweepPlanSource, settings)
	task.Owner, task.SmartWalletAddress, task.MaxExecution = spOwner.Hex(), spWallet.Hex(), 0
	task.Trigger = &avsproto.TaskTrigger{Id: "trigger", Name: "trigger", TriggerType: &avsproto.TaskTrigger_Manual{}}
	held := []map[string]any{{"nativeToken": true, "balance": "1"}}
	for _, token := range tokens {
		held = append(held, map[string]any{"tokenAddress": token.Hex(), "balance": amount})
	}
	encoded, err := json.Marshal(held)
	require.NoError(t, err)
	balances := customCodeNode("balances")
	balances.GetCustomCode().Config.Source = "return " + string(encoded)
	task.Nodes = append([]*avsproto.TaskNode{balances}, task.Nodes...)
	previous := task.Trigger.Id
	for _, node := range task.Nodes {
		node.Id = node.Name
		if node.GetLoop() != nil {
			node.Type = avsproto.NodeType_NODE_TYPE_LOOP
		} else {
			node.Type = avsproto.NodeType_NODE_TYPE_CUSTOM_CODE
		}
		task.Edges = append(task.Edges, &avsproto.TaskEdge{Source: previous, Target: node.Id})
		previous = node.Id
	}
	task.Nodes[2].GetLoop().GetContractWrite().Config.ContractAbi = helperConvertJSONABIToProtobuf(t, `[{"type":"function","name":"transfer","inputs":[{"name":"to","type":"address"},{"name":"amount","type":"uint256"}],"outputs":[{"type":"bool"}]}]`)
	vm, err := NewVMWithData(&model.Workflow{Task: task}, nil, &config.SmartWalletConfig{ChainID: skillSepolia, EthRpcUrl: rpcURL}, nil)
	require.NoError(t, err)
	vm.db = db
	require.NoError(t, vm.Compile())
	return vm
}

func TestSweepPublishesIterationFailures(t *testing.T) {
	tokens := []common.Address{common.HexToAddress("0x11"), common.HexToAddress("0x22"), common.HexToAddress("0x33")}
	const amount = "10000000000000001"
	const remaining = "10000000000000000"
	for _, scenario := range []string{"token cap", "gas cap and unreadable token", "revoked budget", "revoked at signing"} {
		t.Run(scenario, func(t *testing.T) {
			policy := spPolicy("budget", spWallet, 1, model.SessionPolicyActive)
			policy.Sizing, policy.ChainID, policy.Grant.AppliedAt = model.BudgetSizingBudget, skillSepolia, 1
			chain := &sweepBudgetRPC{tokens: map[common.Address]*big.Int{}}
			for _, token := range tokens {
				policy.AllowedActions = append(policy.AllowedActions, model.AllowedAction{Target: &token, Selectors: []string{selectorTransfer}})
				policy.ERC20SpendCaps = append(policy.ERC20SpendCaps, model.ERC20SpendCap{Token: &token, Amount: amount})
				chain.tokens[token], _ = new(big.Int).SetString(amount, 10)
			}
			chain.tokens[tokens[1]], _ = new(big.Int).SetString(remaining, 10)
			if scenario == "gas cap and unreadable token" {
				policy.NativeSpendCap = &model.NativeSpendCap{Amount: "1"}
				chain.native, chain.tokens[tokens[1]] = big.NewInt(1), nil
			}
			db := testutil.TestMustDB()
			t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })
			require.NoError(t, StoreSessionPolicy(db, policy))
			if scenario == "revoked budget" {
				_, _, err := RevokeSessionGrant(db, policy)
				require.NoError(t, err)
			}

			api := rpc.NewServer()
			require.NoError(t, api.RegisterName("eth", chain))
			server := httptest.NewServer(api)
			t.Cleanup(server.Close)
			vm := newSweepTestVM(t, db, server.URL, tokens, amount)
			var submitted []common.Address
			keyFor, _ := spKeyFor(t)
			resolve := NewSessionResolver(db, keyFor, nil)
			signAttempts := 0
			previous := sendUserOpAuto
			t.Cleanup(func() { sendUserOpAuto = previous })
			sendUserOpAuto = func(_ *config.SmartWalletConfig, _ common.Address, data []byte, _ *common.Address, _ *big.Int, _ logger.Logger) (*preset.SentUserOp, *types.Receipt, error) {
				if scenario == "revoked at signing" {
					signAttempts++
					if _, _, err := RevokeSessionGrant(db, policy); err != nil {
						return nil, nil, err
					}
					if _, err := resolve(policy.ChainID, *policy.Owner, *policy.Runner); err != nil {
						return nil, nil, fmt.Errorf("resolving session authorization: %w", err)
					}
				}
				submitted = append(submitted, common.BytesToAddress(data[4:36]))
				return wellFormedUserOp(spWallet), minedReceipt(1), nil
			}
			require.NoError(t, vm.Run())
			require.True(t, dependencyStep(t, vm, "sweepPlan").Success)
			step := dependencyStep(t, vm, "sendTokens")
			skipped := dependencyStep(t, vm, "sendEth")
			require.Equal(t, avsproto.ErrorCode_WRITE_PREREQUISITE_FAILED, skipped.ErrorCode)
			require.Equal(t, step.Id, loopMetadata(skipped)["prerequisiteNodeId"])
			require.False(t, step.Success)
			results := step.GetLoop().Data.AsInterface().([]any)
			require.Len(t, results, 3)
			failures := publishedLoopFailures(t, vm, step.Id)
			if scenario == "token cap" {
				require.Equal(t, []common.Address{tokens[0], tokens[2]}, submitted, "%s", step.Log)
				require.NotNil(t, results[0])
				require.Nil(t, results[1])
				require.NotNil(t, results[2])
				const message = "The token budget is used up. Change the budget to continue."
				require.Equal(t, "1 of 3 iterations failed: "+message, step.Error)
				require.Equal(t, []any{map[string]any{
					"index": float64(1), "errorCode": "BUDGET_USED_UP", "error": message,
					"errorDetails": map[string]any{"token": tokens[1].Hex(), "needed": amount, "remaining": remaining},
				}}, failures)
				return
			}
			if scenario == "revoked at signing" {
				require.Equal(t, 1, signAttempts, "later iterations must be refused during preflight")
			}
			require.Empty(t, submitted)
			require.Equal(t, []any{nil, nil, nil}, results)
			require.Len(t, failures, 3)
			for i, item := range failures {
				failure := item.(map[string]any)
				require.EqualValues(t, i, failure["index"])
				require.NotEmpty(t, failure["error"])
				switch {
				case scenario == "revoked budget" || scenario == "revoked at signing":
					require.Equal(t, "BUDGET_REVOKED", failure["errorCode"])
				case i == 1:
					require.Equal(t, "RPC_NODE_ERROR", failure["errorCode"])
					require.Contains(t, failure["error"], BudgetReadUnavailableCode)
				default:
					require.Equal(t, "BUDGET_USED_UP", failure["errorCode"])
					require.Equal(t, map[string]any{"token": budgetNativeToken.Hex(), "needed": big.NewInt(nativePreflightGasUnitsFor(NativeValue, policy)).String(), "remaining": "1"}, failure["errorDetails"])
				}
			}
		})
	}
}

func TestReconciledSweepReportKeepsOnlyBudgetFailure(t *testing.T) {
	var polls atomic.Int32
	rpcVM := reconciliationVM(t, "confirmed", &polls)
	nodes := sweepTask(t, sweepPlanSource, nil).Nodes[1:]
	for _, node := range nodes {
		node.Id, node.Type = node.Name, avsproto.NodeType_NODE_TYPE_LOOP
	}
	vm := dependencyVM(t, nodes, []*avsproto.TaskEdge{{Source: "trigger", Target: "sendTokens"}, {Source: "sendTokens", Target: "sendEth"}})
	vm.smartWalletConfig = rpcVM.smartWalletConfig
	vm.smartWalletConfig.ChainID = skillSepolia
	vm.task.Name = "Sweep"
	parent := &avsproto.Execution_Step{Id: "sendTokens", Name: "sendTokens"}
	data, err := structpb.NewValue([]any{nil, nil})
	require.NoError(t, err)
	parent.OutputData = &avsproto.Execution_Step_Loop{Loop: &avsproto.LoopNode_Output{Data: data}}
	pending := singlePendingWrite(t, "sendTokens_iter_0")
	blocked := &avsproto.Execution_Step{Id: "sendTokens_iter_1"}
	finalizeStep(blocked, false, NewBudgetUsedUpError(testTokenAddr, big.NewInt(7), big.NewInt(3)), "", "")
	parent.Metadata, err = structpb.NewValue(map[string]any{"iterationFailures": []any{loopIterationFailure(0, pending, nil), loopIterationFailure(1, blocked, nil)}})
	require.NoError(t, err)
	propagateLoopConfirmationFailure(parent, []*avsproto.Execution_Step{pending, blocked})
	vm.addExecutionLog(parent)
	_, err = vm.executeNode(nodes[1])
	require.NoError(t, err)
	require.Positive(t, polls.Load())
	require.Equal(t, avsproto.ErrorCode_WRITE_PREREQUISITE_FAILED, dependencyStep(t, vm, "sendEth").ErrorCode)
	results := parent.GetLoop().Data.AsInterface().([]any)
	require.NotNil(t, results[0])
	require.Nil(t, results[1])
	failures := publishedLoopFailures(t, vm, parent.Id)
	require.Len(t, failures, 1)
	require.Equal(t, float64(1), failures[0].(map[string]any)["index"])
	require.Equal(t, "BUDGET_USED_UP", failures[0].(map[string]any)["errorCode"])
}

func TestRevokedBudgetResolverAndReplacement(t *testing.T) {
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })
	keyFor, _ := spKeyFor(t)
	resolve := NewSessionResolver(db, keyFor, nil)

	// A proposal that lost its nonce can be newer than the last installed
	// budget. Its retained record alone must not imply the owner revoked one.
	proposal := spPolicy("03-proposal", spWallet, 3, model.SessionPolicyRevoked)
	proposal.Sizing, proposal.AwaitingBudgetActivation = model.BudgetSizingBudget, true
	require.NoError(t, StoreSessionPolicy(db, proposal))
	auth, err := resolve(spChain, spOwner, spWallet)
	require.NoError(t, err)
	require.Nil(t, auth)

	revoked := spPolicy("01-installed", spWallet, 1, model.SessionPolicyActive)
	revoked.Sizing, revoked.Grant.AppliedAt = model.BudgetSizingBudget, 1
	_, _, err = RevokeSessionGrant(db, revoked)
	require.NoError(t, err)
	auth, err = resolve(spChain, spOwner, spWallet)
	require.Error(t, err)
	require.Equal(t, avsproto.ErrorCode_BUDGET_REVOKED, GetErrorCode(err))
	require.True(t, preset.IsClientUserOpFailure(fmt.Errorf("resolving session authorization: %w", err)))
	require.Nil(t, auth)
	policy, err := sessionPolicyForRun(db, spChain, spOwner, spWallet, &SessionGrantReport{})
	require.NoError(t, err)
	require.Nil(t, policy)

	replacement := spPolicy("02-installed", spWallet, 2, model.SessionPolicyActive)
	replacement.Sizing, replacement.Grant.AppliedAt = model.BudgetSizingBudget, 2
	require.NoError(t, StoreSessionPolicy(db, replacement))
	auth, err = resolve(spChain, spOwner, spWallet)
	require.NoError(t, err)
	require.NotNil(t, auth)
	require.Equal(t, replacement.ID, auth.PolicyID)
}

func TestLoopIterationFailureAfterLateReceipt(t *testing.T) {
	for _, outcome := range []string{"confirmed", "reverted"} {
		t.Run(outcome, func(t *testing.T) {
			var polls atomic.Int32
			vm := reconciliationVM(t, outcome, &polls)
			pending := singlePendingWrite(t, "loop_iter_0")
			data, err := structpb.NewValue([]any{nil})
			require.NoError(t, err)
			parent := &avsproto.Execution_Step{Id: "loop", OutputData: &avsproto.Execution_Step_Loop{Loop: &avsproto.LoopNode_Output{Data: data}}}
			parent.Metadata, err = structpb.NewValue(map[string]any{"iterationFailures": []any{loopIterationFailure(0, pending, nil)}})
			require.NoError(t, err)
			propagateLoopConfirmationFailure(parent, []*avsproto.Execution_Step{pending})
			require.True(t, vm.reconcileWrite(parent))
			metadata := loopMetadata(parent)
			require.Len(t, metadata["confirmationOutcomes"], 1)
			if outcome == "confirmed" {
				require.True(t, parent.Success)
				require.NotContains(t, metadata, "iterationFailures")
				return
			}
			require.False(t, parent.Success)
			failures := metadata["iterationFailures"].([]any)
			require.Len(t, failures, 1)
			require.Equal(t, "USER_OP_EXECUTION_REVERTED", failures[0].(map[string]any)["errorCode"])
			require.Equal(t, float64(0), failures[0].(map[string]any)["index"])
		})
	}
}

func TestLoopIterationFailuresInParallel(t *testing.T) {
	loop, err := CreateNodeFromType(NodeTypeLoop, map[string]any{
		"inputVariable": "{{items}}", "iterVal": "item", "iterKey": "index", "executionMode": "parallel",
		"runner": map[string]any{"type": "customCode", "config": map[string]any{
			"lang": "javascript", "source": "if (item !== 'ok') throw new Error(item); return item;",
		}},
	}, "parallelLoop")
	require.NoError(t, err)
	step, err := NewVM().RunNodeWithInputs(loop, map[string]any{"items": []any{"first failed", "ok", "last failed"}})
	require.NoError(t, err)
	require.False(t, step.Success)
	results := step.GetLoop().GetData().AsInterface().([]any)
	require.Equal(t, []any{nil, "ok", nil}, results)
	failures := loopMetadata(step)["iterationFailures"].([]any)
	require.Len(t, failures, 2)
	for i, message := range []string{"first failed", "last failed"} {
		entry := failures[i].(map[string]any)
		require.Equal(t, float64(i*2), entry["index"])
		require.Contains(t, entry["error"], message)
	}
}
