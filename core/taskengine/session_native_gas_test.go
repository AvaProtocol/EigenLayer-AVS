package taskengine

import (
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestNativeGrantMaxFee(t *testing.T) {
	cases := []struct {
		chain int64
		want  int64
	}{
		{1, 50_000_000_000},
		{11155111, 2_000_000_000},
		{8453, 1_000_000_000},
		{84532, 100_000_000},
		{137, 2_000_000_000},
	}
	for _, tc := range cases {
		if got := NativeGrantMaxFee(tc.chain); got.Cmp(big.NewInt(tc.want)) != 0 {
			t.Fatalf("chain %d fee = %s, want %d", tc.chain, got, tc.want)
		}
	}
}

func TestSelfFundedEthPayIncludesGasOnce(t *testing.T) {
	now := skillNow()
	fee := big.NewInt(1)
	a1 := "0x0000000000000000000000000000000000000001"
	a2 := "0x0000000000000000000000000000000000000002"
	task := loopEthTask(t, "{{settings.recipients}}", "{{value}}", "{{settings.token_amount.amount}}", 12, map[string]any{
		"recipients":   []any{a1, a2},
		"token_amount": map[string]any{"amount": "1000000000000000"},
	})
	perms, changes, err := MergeSkillGrantWithRemainder(nil, nil, PolicyAddition{}, []*avsproto.Task{task}, skillSepolia, now, time.Hour, WithSelfFundedGrantFee(skillSepolia, fee))
	if err != nil {
		t.Fatal(err)
	}
	// 1e15 * 12 * 2, plus 24*500k steady and one 1.5M install, at 1 wei per gas.
	if perms.NativeSpendCap == nil || perms.NativeSpendCap.Amount != "24000000013500000" {
		t.Fatalf("self-funded cap = %+v", perms.NativeSpendCap)
	}
	if !summaryHas(changes, "self-funded gas") || !summaryHas(changes, "1 wei per gas unit") {
		t.Fatalf("summary = %#v", changes.Summary)
	}

	plain, _, err := MergeSkillGrantWithRemainder(nil, nil, PolicyAddition{}, []*avsproto.Task{task}, skillSepolia, now, time.Hour, WithSelfFundedGrantFee(skillSepolia, nil))
	if err != nil {
		t.Fatal(err)
	}
	if plain.NativeSpendCap == nil || plain.NativeSpendCap.Amount != "24000000000000000" {
		t.Fatalf("nil fee must stay value-only, got %+v", plain.NativeSpendCap)
	}

	one := loopEthTask(t, "{{settings.recipients}}", "{{value}}", "{{settings.token_amount.amount}}", 24, map[string]any{
		"recipients":   []any{a1},
		"token_amount": map[string]any{"amount": "1"},
	})
	two := loopEthTask(t, "{{settings.recipients}}", "{{value}}", "{{settings.token_amount.amount}}", 12, map[string]any{
		"recipients":   []any{a2},
		"token_amount": map[string]any{"amount": "1"},
	})
	one.Id, two.Id = "eth-a", "eth-b"
	both, _, err := MergeSkillGrantWithRemainder(nil, nil, PolicyAddition{}, []*avsproto.Task{one, two}, skillSepolia, now, time.Hour, WithSelfFundedGrantFee(skillSepolia, fee))
	if err != nil {
		t.Fatal(err)
	}
	// 36 wei of value plus one install: 36*500k would be two installs over.
	if both.NativeSpendCap == nil || both.NativeSpendCap.Amount != "19500036" {
		t.Fatalf("two tasks one install = %+v", both.NativeSpendCap)
	}
	in := SessionPolicyInput{ChainID: skillSepolia, Permissions: both}
	if _, err := classifyRunnerCoverage(in, []*avsproto.Task{one, two}, now, "", WithSelfFundedGrantFee(skillSepolia, fee)); err != nil {
		t.Fatalf("a grant merged with one install must still cover both tasks, got %v", err)
	}
	valueOnly, _, err := MergeSkillGrant(nil, PolicyAddition{}, []*avsproto.Task{one, two}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	in.Permissions = valueOnly
	if _, err := classifyRunnerCoverage(in, []*avsproto.Task{one, two}, now, "", WithSelfFundedGrantFee(skillSepolia, fee)); err == nil {
		t.Fatal("a value-only grant must not cover self-funded gas")
	}
}

func TestSiblingContractWriteGasSharesOneInstall(t *testing.T) {
	now := skillNow()
	fee := big.NewInt(1)
	a1 := "0x0000000000000000000000000000000000000001"
	a2 := "0x0000000000000000000000000000000000000002"
	eth := loopEthTask(t, "{{settings.recipients}}", "{{value}}", "{{settings.token_amount.amount}}", 12, map[string]any{
		"recipients":   []any{a1, a2},
		"token_amount": map[string]any{"amount": "1000000000000000"},
	})
	usdc := weeklyPayTask(t, "{{settings.token_amount.address}}", "1000000", []any{a1})
	perms, _, err := MergeSkillGrantWithRemainder(nil, nil, PolicyAddition{}, []*avsproto.Task{eth, usdc}, skillSepolia, now, time.Hour, WithSelfFundedGrantFee(skillSepolia, fee))
	if err != nil {
		t.Fatal(err)
	}
	// ETH value plus 24*500k + 12*1.5M + the larger install (1.5M).
	if perms.NativeSpendCap == nil || perms.NativeSpendCap.Amount != "24000000031500000" {
		t.Fatalf("sibling gas cap = %+v", perms.NativeSpendCap)
	}

	only, changes, err := MergeSkillGrantWithRemainder(nil, nil, PolicyAddition{}, []*avsproto.Task{usdc}, skillSepolia, now, time.Hour, WithSelfFundedGrantFee(skillSepolia, fee))
	if err != nil {
		t.Fatal(err)
	}
	if only.NativeSpendCap != nil {
		t.Fatalf("an ERC-20 grant must not grow a native cap, got %+v", only.NativeSpendCap)
	}
	if summaryHas(changes, "self-funded gas") {
		t.Fatalf("ERC-20 summary included gas: %#v", changes.Summary)
	}

	zero, _, err := MergeSkillGrantWithRemainder(nil, nil, PolicyAddition{}, []*avsproto.Task{skillValueTask("0")}, skillSepolia, now, time.Hour, WithSelfFundedGrantFee(skillSepolia, fee))
	if err != nil {
		t.Fatal(err)
	}
	if zero.NativeSpendCap != nil {
		t.Fatalf("a zero-value write must not install a native cap, got %+v", zero.NativeSpendCap)
	}
}

func TestUnknownRunsDoNotInventGas(t *testing.T) {
	now := skillNow()
	a1 := "0x0000000000000000000000000000000000000001"
	eth := loopEthTask(t, "{{settings.recipients}}", "{{value}}", "{{settings.token_amount.amount}}", 12, map[string]any{
		"recipients":   []any{a1},
		"token_amount": map[string]any{"amount": "1"},
	})
	swap := skillWriteTask("swap", "Open Swap", "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E", "0x04e45aaf", skillSepolia, 0, 0, 0)
	perms, changes, err := MergeSkillGrantWithRemainder(nil, nil, PolicyAddition{}, []*avsproto.Task{eth, swap}, skillSepolia, now, time.Hour, WithSelfFundedGrantFee(skillSepolia, big.NewInt(1)))
	if err != nil {
		t.Fatal(err)
	}
	// 12 wei plus 12*500k and one install. The swap's run count is unknown.
	if perms.NativeSpendCap == nil || perms.NativeSpendCap.Amount != "7500012" {
		t.Fatalf("cap = %+v", perms.NativeSpendCap)
	}
	if !summaryHas(changes, "Open Swap") || !summaryHas(changes, "remaining runs are unknown") {
		t.Fatalf("summary = %#v", changes.Summary)
	}
}

func TestCarriedGrantAddsSiblingGasBeforeTheRemainder(t *testing.T) {
	now := skillNow()
	a1 := "0x0000000000000000000000000000000000000001"
	usdc := weeklyPayTask(t, "{{settings.token_amount.address}}", "1000000", []any{a1})
	owner := common.HexToAddress(a1)
	current := usableGrant("01NATIVE00000000000000000", now.Add(48*time.Hour).UnixMilli(), nil, nil)
	current.Owner = &owner
	current.NativeSpendCap = &model.NativeSpendCap{Amount: "100"}
	current.NativeRecipients = []*common.Address{&owner}
	remainder := &GrantRemainder{Native: big.NewInt(100)}
	perms, _, err := MergeSkillGrantWithRemainder(current, remainder, PolicyAddition{}, []*avsproto.Task{usdc}, skillSepolia, now, time.Hour, WithSelfFundedGrantFee(skillSepolia, big.NewInt(1)))
	if err != nil {
		t.Fatal(err)
	}
	// max(remainder 100, 12*1.5M + one install).
	if perms.NativeSpendCap == nil || perms.NativeSpendCap.Amount != "19500000" {
		t.Fatalf("carried sibling gas = %+v", perms.NativeSpendCap)
	}
	plain, _, err := MergeSkillGrantWithRemainder(current, remainder, PolicyAddition{}, []*avsproto.Task{usdc}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if plain.NativeSpendCap == nil || plain.NativeSpendCap.Amount != "100" {
		t.Fatalf("nil fee must keep the remainder, got %+v", plain.NativeSpendCap)
	}
}

func TestSimulateMissingNativeRecipient(t *testing.T) {
	alice := common.HexToAddress("0x0000000000000000000000000000000000000001")
	bob := common.HexToAddress("0x0000000000000000000000000000000000000002")
	need := &WorkflowNeed{
		HasFundMove:      true,
		Name:             "Weekly ETH Pay",
		NativeRecipients: []*common.Address{&alice},
		NativeSpendCap:   &model.NativeSpendCap{Amount: "1"},
	}
	policy := &model.SessionPolicy{
		ID:               "01GRANT",
		NativeRecipients: []*common.Address{&bob},
		NativeSpendCap:   &model.NativeSpendCap{Amount: "10"},
	}
	got := BuildAuthorization(policy, need, nil, SkillSchedule{})
	if got.Status != AuthNotCovered || !strings.Contains(got.Detail, SessionPolicyRecipientNotAllowedCode) {
		t.Fatalf("missing recipient = %#v", got)
	}
	unread := *need
	unread.Unresolved = true
	if unresolved := BuildAuthorization(policy, &unread, nil, SkillSchedule{}); unresolved.Status != AuthTargetUnresolved {
		t.Fatalf("unresolved must outrank a missing recipient, got %#v", unresolved)
	}
}

func TestJSONNumberSizesAndImpreciseFloatDoesNot(t *testing.T) {
	got, ok := scalarString(json.Number("1000000000000000000"))
	if !ok || got != "1000000000000000000" {
		t.Fatalf("json.Number = %q %v", got, ok)
	}
	if _, ok := scalarString(json.Number("1.5")); ok {
		t.Fatal("a fractional json.Number must not size a cap")
	}
	now := skillNow()
	a1 := "0x0000000000000000000000000000000000000001"
	task := loopEthTask(t, "{{settings.recipients}}", "{{value}}", "{{settings.token_amount.amount}}", 12, map[string]any{
		"recipients":   []any{a1},
		"token_amount": map[string]any{"amount": "1"},
	})
	need := DeriveWorkflowNeeds(task, map[string]any{
		"recipients":   []any{a1},
		"token_amount": map[string]any{"amount": json.Number("1000000000000000000")},
	}, scheduleFromTask(task, now), skillSepolia)[skillSepolia]
	if need == nil || need.NativeUnsized || need.NativeSpendCap == nil || need.NativeSpendCap.Amount != "12000000000000000000" {
		t.Fatalf("json.Number need = %#v", need)
	}

	huge := loopEthTask(t, "{{settings.recipients}}", a1, "{{settings.token_amount.amount}}", 12, map[string]any{
		"recipients":   []any{a1},
		"token_amount": map[string]any{"amount": float64(1 << 54)},
	})
	rec := common.HexToAddress(a1)
	_, _, err := MergeSkillGrant(nil, PolicyAddition{
		NativeRecipients: []*common.Address{&rec},
		NativeSpendCap:   &model.NativeSpendCap{Amount: "1"},
	}, []*avsproto.Task{huge}, skillSepolia, now, time.Hour)
	var conflict *PolicyConflictError
	if !errors.As(err, &conflict) || conflict.Code != SessionPolicyNativeUnsizedCode || !strings.Contains(err.Error(), "decimal string") {
		t.Fatalf("imprecise float = %v", err)
	}
}

func TestTaskValidUntilAddsADayAfterTheLastFire(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	crons := []string{"0 0 * * *"}
	got := taskValidUntil(SkillSchedule{MaxExecution: 2, Now: now}, crons)
	want := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC).UnixMilli()
	if got != want {
		t.Fatalf("validUntil = %s, want %s", time.UnixMilli(got).UTC(), time.UnixMilli(want).UTC())
	}
	expiredAt := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	if kept := taskValidUntil(SkillSchedule{MaxExecution: 2, Now: now, ExpiredAt: expiredAt}, crons); kept != expiredAt {
		t.Fatalf("expiredAt = %d, want the caller's buffer", kept)
	}
}

func TestTooManyNativeRecipientsHasATypedCode(t *testing.T) {
	recs := make([]*common.Address, MaxNativeRecipients+1)
	for i := range recs {
		addr := common.BytesToAddress([]byte{byte(i + 1)})
		recs[i] = &addr
	}
	err := (SessionPermissions{
		NativeRecipients: recs,
		NativeSpendCap:   &model.NativeSpendCap{Amount: "1"},
		ValidUntilMs:     time.Now().Add(time.Hour).UnixMilli(),
	}).Validate()
	if err == nil || !strings.HasPrefix(err.Error(), SessionPolicyTooManyNativeRecipientsCode+":") || !strings.Contains(err.Error(), "finished automations") {
		t.Fatalf("too many recipients = %v", err)
	}
}

// studioDraftAddition is the addition Studio forwards today: the simulate
// verdict's actions, caps, recipients, and nativeSpendCap. It does not
// echo nativeGasWei. The cap itself already includes steady gas.
func studioDraftAddition(t *testing.T, task *avsproto.Task, policy *model.SessionPolicy) PolicyAddition {
	t.Helper()
	now := skillNow()
	need := DeriveWorkflowNeeds(task, nil, scheduleFromTask(task, now), skillSepolia)[skillSepolia]
	if need == nil {
		t.Fatal("draft has no need")
	}
	need.priceFee = big.NewInt(1)
	auth := BuildAuthorization(policy, need, nil, scheduleFromTask(task, now))
	if auth == nil || auth.Required == nil {
		t.Fatal("draft has no required permissions")
	}
	return PolicyAddition{
		AllowedActions:   auth.Required.Actions,
		SpendCaps:        auth.Required.Caps,
		NativeRecipients: auth.Required.NativeRecipients,
		NativeSpendCap:   auth.Required.NativeSpendCap,
	}
}

func TestSkillAddedAfterNativeGrantPricesGas(t *testing.T) {
	now := skillNow()
	fee := big.NewInt(1)
	opt := WithSelfFundedGrantFee(skillSepolia, fee)
	a1 := "0x0000000000000000000000000000000000000001"
	a2 := "0x0000000000000000000000000000000000000002"
	eth := loopEthTask(t, "{{settings.recipients}}", "{{value}}", "{{settings.token_amount.amount}}", 12, map[string]any{
		"recipients":   []any{a1, a2},
		"token_amount": map[string]any{"amount": "1000000000000000"},
	})
	usdc := weeklyPayTask(t, "{{settings.token_amount.address}}", "1000000", []any{a1})
	together, _, err := MergeSkillGrantWithRemainder(nil, nil, PolicyAddition{}, []*avsproto.Task{eth, usdc}, skillSepolia, now, time.Hour, opt)
	if err != nil {
		t.Fatal(err)
	}
	const want = "24000000031500000"

	ethFirst, _, err := MergeSkillGrantWithRemainder(nil, nil, PolicyAddition{}, []*avsproto.Task{eth}, skillSepolia, now, time.Hour, opt)
	if err != nil {
		t.Fatal(err)
	}
	ethPolicy := permissionsAsPolicy(SessionPolicyInput{ChainID: skillSepolia, Permissions: ethFirst})
	usdcDraft := studioDraftAddition(t, usdc, ethPolicy)
	if usdcDraft.NativeSpendCap == nil || usdcDraft.NativeSpendCap.Amount != "18000000" {
		t.Fatalf("USDC draft on a native wallet = %+v, want steady gas 18000000 and no install", usdcDraft.NativeSpendCap)
	}
	ethThenUSDC, changes, err := MergeSkillGrantWithRemainder(ethPolicy, nil, usdcDraft, []*avsproto.Task{eth}, skillSepolia, now, time.Hour, opt)
	if err != nil {
		t.Fatal(err)
	}
	if ethThenUSDC.NativeSpendCap == nil || ethThenUSDC.NativeSpendCap.Amount != want || ethThenUSDC.NativeSpendCap.Amount != together.NativeSpendCap.Amount {
		t.Fatalf("ETH then USDC = %+v, together = %+v", ethThenUSDC.NativeSpendCap, together.NativeSpendCap)
	}
	// Studio does not echo nativeGasWei yet, so the changes split is the
	// ETH task's steady gas plus the one install. The USDC steady gas is
	// inside the addition total.
	if changes.NativeGasWei != "13500000" {
		t.Fatalf("ETH then USDC gas split = %s", changes.NativeGasWei)
	}

	usdcFirst, _, err := MergeSkillGrantWithRemainder(nil, nil, PolicyAddition{}, []*avsproto.Task{usdc}, skillSepolia, now, time.Hour, opt)
	if err != nil {
		t.Fatal(err)
	}
	if usdcFirst.NativeSpendCap != nil {
		t.Fatalf("USDC alone must not install native, got %+v", usdcFirst.NativeSpendCap)
	}
	usdcPolicy := permissionsAsPolicy(SessionPolicyInput{ChainID: skillSepolia, Permissions: usdcFirst})
	ethDraft := studioDraftAddition(t, eth, nil)
	if ethDraft.NativeSpendCap == nil || ethDraft.NativeSpendCap.Amount != "24000000012000000" {
		t.Fatalf("ETH draft = %+v, want value plus steady gas and no install", ethDraft.NativeSpendCap)
	}
	usdcThenETH, echoed, err := MergeSkillGrantWithRemainder(usdcPolicy, nil, ethDraft, []*avsproto.Task{usdc}, skillSepolia, now, time.Hour, opt)
	if err != nil {
		t.Fatal(err)
	}
	if usdcThenETH.NativeSpendCap == nil || usdcThenETH.NativeSpendCap.Amount != want {
		t.Fatalf("USDC then ETH = %+v, want %s", usdcThenETH.NativeSpendCap, want)
	}
	if echoed.NativeGasWei != "19500000" {
		t.Fatalf("USDC then ETH gas split = %s, want the USDC steady gas plus one install", echoed.NativeGasWei)
	}

	ethDraft.NativeGasWei = "12000000"
	_, labeled, err := MergeSkillGrantWithRemainder(usdcPolicy, nil, ethDraft, []*avsproto.Task{usdc}, skillSepolia, now, time.Hour, opt)
	if err != nil {
		t.Fatal(err)
	}
	if labeled.NativeGasWei != "31500000" {
		t.Fatalf("echoed addition gas = %s, want steady gas of both skills plus one install", labeled.NativeGasWei)
	}
}

func TestAppliedGrantDoesNotChargeInstallAgain(t *testing.T) {
	now := skillNow()
	fee := big.NewInt(1)
	a1 := "0x0000000000000000000000000000000000000001"
	a2 := "0x0000000000000000000000000000000000000002"
	eth := loopEthTask(t, "{{settings.recipients}}", "{{value}}", "{{settings.token_amount.amount}}", 12, map[string]any{
		"recipients":   []any{a1, a2},
		"token_amount": map[string]any{"amount": "1000000000000000"},
	})
	usdc := weeklyPayTask(t, "{{settings.token_amount.address}}", "1000000", []any{a1})
	current := permissionsAsPolicy(SessionPolicyInput{
		ChainID: skillSepolia,
		Permissions: SessionPermissions{
			NativeSpendCap: &model.NativeSpendCap{Amount: "24000000000000000"},
			NativeRecipients: []*common.Address{
				ptrAddr(t, a1),
				ptrAddr(t, a2),
			},
		},
	})
	current.Grant.AppliedAt = 1
	// The remaining limit has been spent down to the ETH value. Steady gas
	// for the ETH task is still required. The install is already on chain.
	remainderNative, ok := new(big.Int).SetString("24000000000000000", 10)
	if !ok {
		t.Fatal("remainder")
	}
	usdcDraft := studioDraftAddition(t, usdc, current)
	perms, changes, err := MergeSkillGrantWithRemainder(current, &GrantRemainder{Native: remainderNative}, usdcDraft, []*avsproto.Task{eth}, skillSepolia, now, time.Hour, WithSelfFundedGrantFee(skillSepolia, fee))
	if err != nil {
		t.Fatal(err)
	}
	if perms.NativeSpendCap == nil || perms.NativeSpendCap.Amount != "24000000030000000" {
		t.Fatalf("applied grant = %+v, want value + ETH steady + USDC steady and no second install", perms.NativeSpendCap)
	}
	// The addition does not echo nativeGasWei, so the split is the ETH
	// task's steady gas only.
	if changes.NativeGasWei != "12000000" {
		t.Fatalf("applied gas split = %s", changes.NativeGasWei)
	}
	if summaryHas(changes, "one install") {
		t.Fatalf("an applied grant must not claim a new install: %#v", changes.Summary)
	}
}

func TestContractWriteCountsEachMethodCall(t *testing.T) {
	now := skillNow()
	call := "0x04e45aaf"
	task := &avsproto.Task{
		Id:           "two-calls",
		Name:         "Two calls",
		MaxExecution: 2,
		Nodes: []*avsproto.TaskNode{{
			TaskType: &avsproto.TaskNode_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode{
					Config: &avsproto.ContractWriteNode_Config{
						ContractAddress: "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E",
						ChainId:         skillSepolia,
						MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
							{MethodName: "exactInputSingle", CallData: &call},
							{MethodName: "refundETH", CallData: &call},
						},
					},
				},
			},
		}},
	}
	need := DeriveWorkflowNeeds(task, nil, scheduleFromTask(task, now), skillSepolia)[skillSepolia]
	if need == nil || need.NativeValueOps != 4 || need.NativeSendOps != 0 {
		t.Fatalf("two calls over two runs = %+v", need)
	}
	alone, _, err := MergeSkillGrantWithRemainder(nil, nil, PolicyAddition{}, []*avsproto.Task{task}, skillSepolia, now, time.Hour, WithSelfFundedGrantFee(skillSepolia, big.NewInt(1)))
	if err != nil {
		t.Fatal(err)
	}
	if alone.NativeSpendCap != nil {
		t.Fatalf("method calls with no native value must not install a native cap, got %+v", alone.NativeSpendCap)
	}

	a1 := "0x0000000000000000000000000000000000000001"
	eth := loopEthTask(t, "{{settings.recipients}}", "{{value}}", "{{settings.token_amount.amount}}", 1, map[string]any{
		"recipients":   []any{a1},
		"token_amount": map[string]any{"amount": "1"},
	})
	perms, _, err := MergeSkillGrantWithRemainder(nil, nil, PolicyAddition{}, []*avsproto.Task{eth, task}, skillSepolia, now, time.Hour, WithSelfFundedGrantFee(skillSepolia, big.NewInt(1)))
	if err != nil {
		t.Fatal(err)
	}
	// 1 wei, plus 4 value ops and 1 send op, plus one install.
	if perms.NativeSpendCap == nil || perms.NativeSpendCap.Amount != "8000001" {
		t.Fatalf("per-call gas = %+v, want 8000001", perms.NativeSpendCap)
	}
}

func TestSimulatePricesAfterObservedSpendAndExistingNativeCap(t *testing.T) {
	now := skillNow()
	a1 := "0x0000000000000000000000000000000000000001"
	usdc := weeklyPayTask(t, "{{settings.token_amount.address}}", "1000000", []any{a1})
	policy := usableGrant("01NATIVE00000000000000000", now.Add(48*time.Hour).UnixMilli(), nil, nil)
	policy.NativeSpendCap = &model.NativeSpendCap{Amount: "1"}
	draft := studioDraftAddition(t, usdc, policy)
	if draft.NativeSpendCap == nil || draft.NativeSpendCap.Amount != "18000000" || len(draft.NativeRecipients) != 0 {
		t.Fatalf("ERC-20 draft on a native grant = %+v recipients %d", draft.NativeSpendCap, len(draft.NativeRecipients))
	}

	// The static walk could not size the send. Simulation observed 1 wei,
	// and gas is priced only after that cap exists.
	need := &WorkflowNeed{
		HasFundMove:   true,
		Name:          "Weekly ETH Pay",
		NativeSendOps: 12,
		NativeUnsized: true,
		priceFee:      big.NewInt(1),
	}
	report := &SessionGrantReport{}
	report.observeNative(common.HexToAddress(a1), big.NewInt(1))
	auth := BuildAuthorization(nil, need, report, SkillSchedule{MaxExecution: 12, Now: now})
	if auth == nil || auth.Required == nil || auth.Required.NativeSpendCap == nil {
		t.Fatalf("observed native = %#v", auth)
	}
	if auth.Required.NativeSpendCap.Amount != "6000012" {
		t.Fatalf("observed cap = %s, want 12 wei plus 12 steady sends", auth.Required.NativeSpendCap.Amount)
	}
	if auth.Required.NativeGasWei == nil || auth.Required.NativeGasWei.String() != "6000000" {
		t.Fatalf("observed gas = %v", auth.Required.NativeGasWei)
	}
}

func TestFormatGrantFeeUsesGweiWhenWhole(t *testing.T) {
	if got := formatGrantFee(big.NewInt(50_000_000_000)); got != "50 gwei" {
		t.Fatalf("50 gwei = %q", got)
	}
	if got := formatGrantFee(big.NewInt(1)); got != "1 wei" {
		t.Fatalf("1 wei = %q", got)
	}
	fee := big.NewInt(2_000_000_000)
	if got := formatGrantFee(fee); got != "2 gwei" || fee.String() != "2000000000" {
		t.Fatalf("format mutated or mislabeled fee %s as %q", fee, got)
	}
}

func TestNativeGasWeiCannotExceedTheAdditionCap(t *testing.T) {
	now := skillNow()
	rec := common.HexToAddress("0x0000000000000000000000000000000000000001")
	_, _, err := MergeSkillGrant(nil, PolicyAddition{
		NativeRecipients: []*common.Address{&rec},
		NativeSpendCap:   &model.NativeSpendCap{Amount: "10"},
		NativeGasWei:     "11",
	}, nil, skillSepolia, now, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "nativeGasWei exceeds nativeSpendCap") {
		t.Fatalf("over-declared gas = %v", err)
	}
}

func ptrAddr(t *testing.T, hex string) *common.Address {
	t.Helper()
	addr := common.HexToAddress(hex)
	return &addr
}

func summaryHas(changes PolicyChanges, part string) bool {
	for _, line := range changes.Summary {
		if strings.Contains(line, part) {
			return true
		}
	}
	return false
}
