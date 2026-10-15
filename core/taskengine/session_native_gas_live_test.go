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

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// TestWeeklyEthPayAndUsdcShareNativeCap_Sepolia is the live check for a
// weekly ETH loop and a second ERC-20 automation on one self-funded runner.
//
// Both tasks are crons. Prepare sizes one native cap for the ETH the loop
// sends and for both automations' gas, including one install. The test then
// sends one ETH transfer and one USDC transfer. The USDC transfer must
// decrease the same native limit: that is the gas the second automation
// spends. On-demand, not per-PR CI. Self-funded; no Gas Manager.
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
	seedEnabledSkillTask(t, env.db, env.owner, env.runner, eth)
	seedEnabledSkillTask(t, env.db, env.owner, env.runner, usdc)

	prepared, err := env.engine.PrepareSessionPolicy(env.user, SessionPolicyInput{
		Wallet: env.runner, ChainID: env.chainID, AgentLabel: "WeeklyPayLive",
		Addition: &PolicyAddition{}, ExpiresInSeconds: 24 * 3600,
	})
	require.NoError(t, err)
	require.NotNil(t, prepared.EchoPermissions)
	require.NotNil(t, prepared.EchoPermissions.NativeSpendCap)
	want := weeklySelfFundedCap(runs, 1, mustWei(t, sendWei), NativeGrantMaxFee(env.chainID))
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

	ethResult, err := runETHTransfer(env, env.owner.Hex(), sendWei)
	if err != nil {
		logSepoliaBaseFee(t, env)
	}
	require.NoError(t, err, "one loop iteration of Weekly ETH Pay")
	logLiveOp(t, "ETH send", ethResult)
	afterETH, err := readNativeLimit(env.client, stored.EntityID, env.runner)
	require.NoError(t, err)
	require.Positive(t, afterETH.Sign(), "install must leave a native limit")
	require.Negative(t, afterETH.Cmp(want), "the ETH send must spend value and gas from the cap, left %s of %s", afterETH, want)
	t.Logf("after ETH send, native limit %s", afterETH)

	usdcAmount := "0"
	if bal := readERC20Balance(t, env, common.HexToAddress(SEPOLIA_USDC)); bal.Sign() > 0 {
		usdcAmount = "1"
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
	logRunnerBalance(t, env, "after")
	t.Log("PASS: weekly ETH loop and a second ERC-20 automation share one self-funded native cap")
}

func withWeeklyCron(task *avsproto.Task, expiredAt int64) {
	task.ExpiredAt = expiredAt
	task.Trigger = &avsproto.TaskTrigger{
		TriggerType: &avsproto.TaskTrigger_Cron{
			Cron: &avsproto.CronTrigger{
				Config: &avsproto.CronTrigger_Config{Schedules: []string{"0 12 * * 1"}},
			},
		},
	}
}

// weeklySelfFundedCap is the ETH the loop sends, plus steady gas for one
// send and one ERC-20 call per recipient per run, plus one install, at fee.
// The send premium and the payable premium are the same 1.5M units, and the
// merge keeps only one of them.
func weeklySelfFundedCap(runs, recipients int64, perSend, fee *big.Int) *big.Int {
	value := new(big.Int).Mul(big.NewInt(runs*recipients), new(big.Int).Set(perSend))
	units := runs * recipients * (nativePreflightGasUnitsSteady + nativePreflightGasUnitsValueSteady)
	units += nativePreflightGasUnitsFirstOp - nativePreflightGasUnitsSteady
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
