//go:build integration

package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// TestWeeklyEthPayAndUsdcShareNativeCap_Sepolia is the live check for a
// weekly ETH loop and a second ERC-20 automation on one self-funded runner.
//
// Both tasks are Monday-noon crons with maxExecution 12 and expiredAt 120
// days out. Prepare sizes one native cap for the ETH the loop sends and for
// both automations' gas, including one install. The test then fires the
// seeded ETH task once through TriggerWorkflow. That is one tick of the
// executor path a scheduled run uses: the loop binds its recipient and the
// ETH send runs the session-grant preflight. The USDC cron only sizes the
// cap. The shared-cap draw is one contractWrite transfer. On-demand, not
// per-PR CI. Self-funded; no Gas Manager.
//
//	CGO_ENABLED=0 go test -tags=integration ./core/taskengine -run TestWeeklyEthPayAndUsdcShareNativeCap_Sepolia -v -count=1
//
// Uses the salt-24 fixture. The submitted grant replaces whatever is usable
// on that runner. The next L1–L11 run submits its own grant again.
func TestWeeklyEthPayAndUsdcShareNativeCap_Sepolia(t *testing.T) {
	env := setupNativeLive(t)
	cfg := env.engine.ResolveSmartWalletConfig(env.chainID)
	require.NotNil(t, cfg)
	require.Empty(t, cfg.SponsorshipPolicyID(), "this check is the self-funded path")
	logRunnerBalance(t, env, "before")
	logSepoliaBaseFee(t, env)

	const runs = 12
	const sendWei = "1000000000000" // 1e12 per recipient per run
	expiredAt := time.Now().Add(120 * 24 * time.Hour).UnixMilli()
	eth := loopEthTask(t, "{{settings.recipients}}", "{{value}}", "{{settings.token_amount.amount}}", runs, map[string]any{
		"recipients":   []any{env.owner.Hex()},
		"token_amount": map[string]any{"amount": sendWei},
	})
	usdc := weeklyPayTask(t, "{{settings.token_amount.address}}", "1", []any{env.owner.Hex()})
	withWeeklyCron(eth, expiredAt)
	withWeeklyCron(usdc, expiredAt)
	wireEthLoop(eth)
	seedEnabledSkillTask(t, env.db, env.owner, env.runner, eth)
	seedEnabledSkillTask(t, env.db, env.owner, env.runner, usdc)

	prepared, err := env.engine.PrepareSessionPolicy(env.user, SessionPolicyInput{
		Wallet: env.runner, ChainID: env.chainID, AgentLabel: "WeeklyPayLive",
		Addition:         &PolicyAddition{},
		ExpiresInSeconds: 24 * 3600, // starting horizon; taskValidUntil returns expiredAt, and the merge keeps the later end, so the 120-day tasks win
	})
	require.NoError(t, err)
	require.NotNil(t, prepared.EchoPermissions)
	require.NotNil(t, prepared.EchoPermissions.NativeSpendCap)
	// One native recipient and the USDC target: the shape the merge sizes the install for.
	want := weeklySelfFundedCap(runs, 1, mustWei(t, sendWei), NativeGrantMaxFee(env.chainID), grantGasShape{nativeRows: 1, targets: 1})
	require.Equal(t, want.String(), prepared.EchoPermissions.NativeSpendCap.Amount,
		"cap must be ETH value plus both automations' steady gas and one install")
	require.Len(t, prepared.EchoPermissions.NativeRecipients, 1)
	require.NotEmpty(t, prepared.EchoPermissions.SpendCaps)
	require.Equal(t, "12", prepared.EchoPermissions.SpendCaps[0].Amount, "1 USDC unit times 12 runs")
	if prepared.SkillChanges != nil {
		t.Logf("summary: %v", prepared.SkillChanges.Summary)
		t.Logf("nativeGasWei: %s", prepared.SkillChanges.NativeGasWei)
	}
	t.Logf("native cap %s", prepared.EchoPermissions.NativeSpendCap.Amount)

	sig, err := crypto.Sign(prepared.Digest.Bytes(), env.ownerKey)
	require.NoError(t, err)
	sig[64] += 27
	echo := *prepared.EchoPermissions
	stored, _, err := env.engine.SubmitSessionPolicy(env.user, SessionPolicyInput{
		Wallet: env.runner, ChainID: env.chainID, AgentLabel: "WeeklyPayLive",
		Permissions: echo,
	}, prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline, sig)
	require.NoError(t, err)
	t.Logf("grant %s entity %d", stored.ID, stored.EntityID)

	before, err := readNativeLimit(env.client, stored.EntityID, env.runner)
	require.NoError(t, err)
	require.Zero(t, before.Sign(), "the new entity is not installed until the first UserOp")

	triggered, err := env.engine.TriggerWorkflow(env.user, &avsproto.TriggerTaskReq{
		TaskId:      eth.Id,
		TriggerType: avsproto.TriggerType_TRIGGER_TYPE_CRON,
		TriggerOutput: &avsproto.TriggerTaskReq_CronTrigger{
			CronTrigger: &avsproto.CronTrigger_Output{},
		},
		IsBlocking: true,
	})
	logTriggeredRun(t, "ETH loop tick", triggered)
	if err != nil {
		logSepoliaBaseFee(t, env)
	}
	require.NoError(t, err, "one tick of the seeded weekly ETH loop")
	require.NotNil(t, triggered)
	require.Equal(t, avsproto.ExecutionStatus_EXECUTION_STATUS_SUCCESS, triggered.Status, "ETH loop tick: %s", triggeredError(triggered))
	// The UserOp send already waited for its receipt. This pause covers RPC
	// lag before the next read of the native limit.
	time.Sleep(2 * time.Second)
	afterETH, err := readNativeLimit(env.client, stored.EntityID, env.runner)
	require.NoError(t, err)
	require.Positive(t, afterETH.Sign(), "install must leave a native limit")
	require.Negative(t, afterETH.Cmp(want), "the ETH send must spend value and gas from the cap, left %s of %s", afterETH, want)
	t.Logf("after ETH send, native limit %s", afterETH)

	usdcAmount := "0"
	if bal := readERC20Balance(t, env, common.HexToAddress(SEPOLIA_USDC)); bal.Sign() > 0 {
		usdcAmount = "1"
	}
	if usdcAmount == "0" {
		t.Log("USDC transfer moves 0 because the runner holds none; that gas is a lower bound for a real weekly payment")
	}
	t.Logf("USDC transfer amount %s", usdcAmount)
	usdcResult, err := runERC20Transfer(env, SEPOLIA_USDC, env.owner.Hex(), usdcAmount)
	if err != nil {
		logSepoliaBaseFee(t, env)
	}
	require.NoError(t, err, "the ERC-20 automation on the same runner")
	logLiveOp(t, "USDC transfer", usdcResult)
	afterUSDC, err := readNativeLimit(env.client, stored.EntityID, env.runner)
	require.NoError(t, err)
	require.Negative(t, afterUSDC.Cmp(afterETH),
		"the ERC-20 UserOp must spend gas from the same native cap, before %s after %s", afterETH, afterUSDC)
	t.Logf("after USDC transfer, native limit %s", afterUSDC)
	// perRun includes the install, so multiplying it by the remaining runs
	// overstates what those runs need. The cap must still cover that.
	perRun := new(big.Int).Sub(new(big.Int).Set(want), afterUSDC)
	require.Positive(t, perRun.Sign(), "the first run must spend from the cap, spent %s", perRun)
	need := new(big.Int).Mul(new(big.Int).Set(perRun), big.NewInt(runs-1))
	require.GreaterOrEqual(t, afterUSDC.Cmp(need), 0,
		"cap must cover the remaining %d runs at the observed first-run spend: left %s need %s",
		runs-1, afterUSDC, need)
	budget := new(big.Int).Mul(
		big.NewInt(nativePreflightGasUnitsSteady+nativePreflightGasUnitsValueSteady),
		NativeGrantMaxFee(env.chainID),
	)
	headroom := new(big.Float).Quo(new(big.Float).SetInt(budget), new(big.Float).SetInt(perRun))
	headroomF, _ := headroom.Float64()
	t.Logf("headroom per run: %.1fx (grant gas budget %s / observed first-run spend %s, install included)", headroomF, budget, perRun)
	t.Logf("left %s; %d more runs at the observed rate need %s", afterUSDC, runs-1, need)
	logRunnerBalance(t, env, "after")
	t.Log("PASS: one tick of the weekly ETH loop and a USDC transfer share one self-funded native cap")
}

// wireEthLoop makes the sized ETH task executable. The sizer reads the loop
// runner and the cron; it does not need a node id or an edge. TriggerWorkflow
// does. IterVal is the name the sizer already defaults a blank iter to, so
// {{value}} binds the recipient when the loop runs.
func wireEthLoop(task *avsproto.Task) {
	task.Id = model.GenerateID()
	node := task.Nodes[0]
	node.Id = "ethLoop"
	node.Name = "ethPay"
	if loop := node.GetLoop(); loop != nil && loop.Config != nil {
		loop.Config.IterVal = "value"
	}
	task.Edges = []*avsproto.TaskEdge{{
		Id:     "cronToEth",
		Source: task.Trigger.GetId(),
		Target: node.Id,
	}}
}

func triggeredError(resp *avsproto.TriggerTaskResp) string {
	if resp == nil || resp.Error == nil {
		return ""
	}
	return *resp.Error
}

func logTriggeredRun(t *testing.T, label string, resp *avsproto.TriggerTaskResp) {
	t.Helper()
	if resp == nil {
		t.Logf("%s: no response", label)
		return
	}
	t.Logf("%s status %s id %s error %s", label, resp.Status, resp.ExecutionId, triggeredError(resp))
	for _, step := range resp.Steps {
		if step == nil {
			continue
		}
		evidence := ""
		if step.Metadata != nil {
			evidence = liveOpEvidence(step.Metadata.AsInterface())
		}
		if loop := step.GetLoop(); loop != nil && loop.Data != nil {
			if more := liveOpEvidence(loop.Data.AsInterface()); more != "" {
				evidence = strings.TrimSpace(evidence + " " + more)
			}
		}
		if ethOut := step.GetEthTransfer(); ethOut != nil && ethOut.Data != nil {
			if more := liveOpEvidence(ethOut.Data.AsInterface()); more != "" {
				evidence = strings.TrimSpace(evidence + " " + more)
			}
		}
		if step.GasUsed != "" || step.TotalGasCost != "" {
			evidence = strings.TrimSpace(evidence + fmt.Sprintf(" gasUsed=%s gasPrice=%s totalGasCost=%s", step.GasUsed, step.GasPrice, step.TotalGasCost))
		}
		t.Logf("step %s success=%v error=%s %s", step.Id, step.Success, step.Error, evidence)
	}
}

func withWeeklyCron(task *avsproto.Task, expiredAt int64) {
	task.ExpiredAt = expiredAt
	task.Trigger = &avsproto.TaskTrigger{
		Id:   "weeklyCron",
		Name: "weeklyCron",
		TriggerType: &avsproto.TaskTrigger_Cron{
			Cron: &avsproto.CronTrigger{
				Config: &avsproto.CronTrigger_Config{Schedules: []string{"0 12 * * 1"}},
			},
		},
	}
}

// weeklySelfFundedCap is the ETH the loop sends, plus steady gas for one
// send and one ERC-20 call per recipient per run, plus one install of a
// grant of that shape, at fee. The send premium and the payable premium are
// the same, and the merge keeps only one of them.
func weeklySelfFundedCap(runs, recipients int64, perSend, fee *big.Int, shape grantGasShape) *big.Int {
	value := new(big.Int).Mul(big.NewInt(runs*recipients), new(big.Int).Set(perSend))
	units := runs * recipients * (nativePreflightGasUnitsSteady + nativePreflightGasUnitsValueSteady)
	units += shape.installPremiumUnits()
	gas := new(big.Int).Mul(big.NewInt(units), fee)
	return value.Add(value, gas)
}

func mustWei(t *testing.T, s string) *big.Int {
	t.Helper()
	n, ok := new(big.Int).SetString(s, 10)
	require.True(t, ok, "wei %s", s)
	return n
}

func readERC20Balance(t *testing.T, env *nativeLiveEnv, token common.Address) *big.Int {
	t.Helper()
	data := append(crypto.Keccak256([]byte("balanceOf(address)"))[:4], common.LeftPadBytes(env.runner.Bytes(), 32)...)
	out, err := env.client.CallContract(context.Background(), ethereum.CallMsg{To: &token, Data: data}, nil)
	require.NoError(t, err)
	return new(big.Int).SetBytes(out)
}

func runERC20Transfer(env *nativeLiveEnv, token, dest, amount string) (map[string]interface{}, error) {
	result, err := env.engine.RunNodeImmediately("contractWrite", map[string]interface{}{
		"contractAddress": token,
		"contractAbi":     erc20TransferABI(),
		"chainId":         env.chainID,
		"methodCalls": []interface{}{
			map[string]interface{}{
				"methodName":   "transfer",
				"methodParams": []interface{}{dest, amount},
			},
		},
	}, map[string]interface{}{
		"settings": map[string]interface{}{
			"runner": env.runner.Hex(), "smartWallet": env.runner.Hex(), "chain_id": env.chainID,
		},
	}, env.user, false)
	if err != nil {
		return result, err
	}
	if success, ok := result["success"].(bool); ok && !success {
		return result, fmt.Errorf("transfer failed: %v", result["error"])
	}
	// The UserOp send already waited for its receipt. This pause covers RPC
	// lag before the next read of the native limit.
	time.Sleep(2 * time.Second)
	return result, nil
}

func erc20TransferABI() []interface{} {
	return []interface{}{
		map[string]interface{}{
			"inputs": []interface{}{
				map[string]interface{}{"name": "to", "type": "address"},
				map[string]interface{}{"name": "amount", "type": "uint256"},
			},
			"name":            "transfer",
			"outputs":         []interface{}{map[string]interface{}{"name": "", "type": "bool"}},
			"stateMutability": "nonpayable",
			"type":            "function",
		},
	}
}

func logRunnerBalance(t *testing.T, env *nativeLiveEnv, when string) {
	t.Helper()
	bal, err := env.client.BalanceAt(context.Background(), env.runner, nil)
	require.NoError(t, err)
	t.Logf("runner balance %s %s", when, bal)
}

func logSepoliaBaseFee(t *testing.T, env *nativeLiveEnv) {
	t.Helper()
	header, err := env.client.HeaderByNumber(context.Background(), nil)
	if err != nil || header == nil || header.BaseFee == nil {
		t.Logf("base fee unavailable: %v", err)
		return
	}
	t.Logf("sepolia base fee %s wei; grant assumption %s wei per gas", header.BaseFee, NativeGrantMaxFee(env.chainID))
}

func logLiveOp(t *testing.T, label string, result map[string]interface{}) {
	t.Helper()
	if result == nil {
		t.Logf("%s: no result", label)
		return
	}
	evidence := liveOpEvidence(result)
	if evidence == "" {
		t.Logf("%s: no hash in the result", label)
		return
	}
	t.Logf("%s: %s", label, evidence)
}

func liveOpEvidence(v any) string {
	var parts []string
	var walk func(any, int)
	walk = func(x any, depth int) {
		if depth > 6 {
			return
		}
		switch m := x.(type) {
		case map[string]interface{}:
			for _, key := range []string{"transactionHash", "txHash", "userOpHash", "hash", "gasUsed", "gasPrice", "totalGasCost", "error", "success"} {
				if val, ok := m[key]; ok && val != nil && fmt.Sprint(val) != "" {
					parts = append(parts, key+"="+fmt.Sprint(val))
				}
			}
			for _, child := range m {
				walk(child, depth+1)
			}
		case []interface{}:
			for _, child := range m {
				walk(child, depth+1)
			}
		}
	}
	walk(v, 0)
	return strings.Join(parts, " ")
}
