package taskengine

import (
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

const (
	skillUSDC    = "0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"
	skillWETH    = "0xfFf9976782d46CC05630D1f6eBAb18b2324d6B14"
	skillSepolia = int64(11155111)
)

func skillNow() time.Time {
	return time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
}

func skillAction(token string, selectors ...string) model.AllowedAction {
	t := common.HexToAddress(token)
	return model.AllowedAction{Target: &t, Selectors: selectors}
}

func skillCap(token, amount string) model.ERC20SpendCap {
	t := common.HexToAddress(token)
	return model.ERC20SpendCap{Token: &t, Amount: amount}
}

func transferCalldata(to common.Address, amount *big.Int) string {
	buf := make([]byte, 68)
	copy(buf[:4], common.FromHex(selectorTransfer))
	copy(buf[16:36], to.Bytes())
	raw := amount.Bytes()
	copy(buf[68-len(raw):], raw)
	return "0x" + common.Bytes2Hex(buf)
}

func skillWriteTask(id, name, contract, calldata string, chain, maxExec, ran, expiredAt int64) *avsproto.Task {
	return &avsproto.Task{
		Id:             id,
		Name:           name,
		MaxExecution:   maxExec,
		ExecutionCount: ran,
		ExpiredAt:      expiredAt,
		Nodes: []*avsproto.TaskNode{{
			TaskType: &avsproto.TaskNode_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode{
					Config: &avsproto.ContractWriteNode_Config{
						ContractAddress: contract,
						CallData:        calldata,
						ChainId:         chain,
					},
				},
			},
		}},
	}
}

func skillValueTask(value string) *avsproto.Task {
	task := skillWriteTask("swap", "Swap", "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E", "0x04e45aaf", skillSepolia, 1, 0, 0)
	task.Nodes[0].GetContractWrite().Config.Value = &value
	return task
}

func usableGrant(id string, until int64, actions []model.AllowedAction, caps []model.ERC20SpendCap) *model.SessionPolicy {
	p := &model.SessionPolicy{
		ID:             id,
		Status:         model.SessionPolicyPending,
		Grant:          &model.SessionGrantAuthorization{},
		AllowedActions: actions,
		ERC20SpendCaps: caps,
		ValidUntil:     until,
	}
	if len(caps) > 0 {
		first := caps[0]
		p.ERC20SpendCap = &first
	}
	return p
}

func TestMergeSkillGrantSizesFromRemainingNotTheOldCap(t *testing.T) {
	now := skillNow()
	dec28 := time.Date(2026, 12, 28, 0, 0, 0, 0, time.UTC)
	nov30 := time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)
	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
	// 7 runs left (10 - 3) at 1 unit, plus the new automation's total of 12.
	// The stored cap of 24 must not be reused and must not be added.
	task := skillWriteTask("swap-1", "Weekly swap", skillUSDC, transferCalldata(payee, big.NewInt(1)), skillSepolia, 10, 3, dec28.UnixMilli())
	current := usableGrant("01OLDGRANT000000000000000", nov30.UnixMilli(), []model.AllowedAction{
		skillAction(skillUSDC, selectorTransfer),
		skillAction(skillWETH, selectorApprove),
	}, []model.ERC20SpendCap{skillCap(skillUSDC, "24"), skillCap(skillWETH, "5")})

	perms, changes, err := MergeSkillGrant(current, PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "12")},
		ValidUntilMs:   time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).UnixMilli(),
	}, []*avsproto.Task{task}, skillSepolia, now, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := perms.SpendCaps; len(got) != 1 || got[0].Amount != "19" {
		t.Fatalf("merged cap = %+v, want USDC 19", got)
	}
	if perms.SpendCap == nil || perms.SpendCap.Amount != "19" {
		t.Fatalf("alias cap = %+v, want 19", perms.SpendCap)
	}
	if perms.ValidUntilMs != dec28.UnixMilli() {
		t.Fatalf("validUntil = %d, want Dec 28", perms.ValidUntilMs)
	}
	if changes.BasePolicyID != current.ID {
		t.Fatalf("base = %q", changes.BasePolicyID)
	}
	usdc := common.HexToAddress(skillUSDC)
	weth := common.HexToAddress(skillWETH)
	wantExpiry := "Weekly swap: until Dec 28, was Nov 30"
	if len(changes.Summary) == 0 || changes.Summary[0] != wantExpiry {
		t.Fatalf("summary = %#v", changes.Summary)
	}
	foundCap := false
	for _, line := range changes.Summary {
		if strings.Contains(line, "36") || strings.Contains(line, "was 19") {
			t.Fatalf("cap was combined with the old total: %q", line)
		}
		if line == "Cap "+usdc.Hex()+": 19 (was 24)" {
			foundCap = true
		}
	}
	if !foundCap {
		t.Fatalf("missing USDC cap line in %#v", changes.Summary)
	}
	if len(changes.RemovedActions) != 1 || changes.RemovedActions[0].Target.Hex() != weth.Hex() {
		t.Fatalf("leftover WETH action = %+v", changes.RemovedActions)
	}
	for _, action := range perms.AllowedActions {
		if action.Target != nil && *action.Target == weth {
			t.Fatal("merged grant kept a WETH action no task needs")
		}
	}
}

func TestMergeSkillGrantFirstGrantHasNoWasLine(t *testing.T) {
	now := skillNow()
	perms, changes, err := MergeSkillGrant(nil, PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "12")},
	}, nil, skillSepolia, now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if changes.BasePolicyID != "" {
		t.Fatalf("first grant base = %q", changes.BasePolicyID)
	}
	if perms.SpendCaps[0].Amount != "12" {
		t.Fatalf("cap = %s", perms.SpendCaps[0].Amount)
	}
	for _, line := range changes.Summary {
		if strings.Contains(line, "was ") {
			t.Fatalf("first grant must not say was: %q", line)
		}
	}
	if len(changes.AddedActions) != 1 {
		t.Fatalf("added = %+v", changes.AddedActions)
	}
}

func TestMergeSkillGrantUnsizedTransferFailsClosed(t *testing.T) {
	task := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(common.HexToAddress("0x0000000000000000000000000000000000000001"), big.NewInt(1)), skillSepolia, 0, 0, 0)
	_, _, err := MergeSkillGrant(nil, PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorApprove)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "1")},
	}, []*avsproto.Task{task}, skillSepolia, skillNow(), time.Hour)
	if !errors.Is(err, ErrSessionPolicyUnsized) {
		t.Fatalf("unlimited transfer must fail closed, got %v", err)
	}
}

func TestMergeSkillGrantUnlimitedSwapUnionsActions(t *testing.T) {
	task := skillWriteTask("swap", "Swap", "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E", "0x04e45aaf", skillSepolia, 0, 0, 0)
	perms, _, err := MergeSkillGrant(nil, PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorApprove)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "12")},
	}, []*avsproto.Task{task}, skillSepolia, skillNow(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(perms.AllowedActions) != 2 {
		t.Fatalf("actions = %+v", perms.AllowedActions)
	}
}

func TestMergeSkillGrantSkipsAnotherChain(t *testing.T) {
	task := skillWriteTask("base", "Base pay", skillUSDC, transferCalldata(common.HexToAddress("0x0000000000000000000000000000000000000001"), big.NewInt(9)), 8453, 3, 0, 0)
	perms, _, err := MergeSkillGrant(nil, PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "12")},
	}, []*avsproto.Task{task}, skillSepolia, skillNow(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if perms.SpendCaps[0].Amount != "12" {
		t.Fatalf("other-chain spend leaked into the cap: %s", perms.SpendCaps[0].Amount)
	}
}

func TestExpiryChangeLine(t *testing.T) {
	same := ExpiryChangeLine("Weekly swap", time.Date(2026, 12, 28, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC))
	if same != "Weekly swap: until Dec 28, was Nov 30" {
		t.Fatalf("same year: %q", same)
	}
	diff := ExpiryChangeLine("", time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC), time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC))
	if diff != "Automation: until Jan 2, 2027, was Nov 30, 2026" {
		t.Fatalf("different year: %q", diff)
	}
}

func TestCoverageRefusal(t *testing.T) {
	now := skillNow()
	later := now.Add(30 * 24 * time.Hour).UnixMilli()
	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
	data := transferCalldata(payee, big.NewInt(1))
	write := skillWriteTask("pay", "Pay", skillUSDC, data, skillSepolia, 1, 0, later)
	needs := DeriveWorkflowNeeds(write, nil, scheduleFromTask(write, now), skillSepolia)
	need := needs[skillSepolia]
	if need == nil || !need.HasFundMove {
		t.Fatal("transfer task produced no need")
	}

	if got := CoverageRefusal(nil, need); got == nil || !errors.Is(got, ErrSessionPolicyNotCovering) {
		t.Fatalf("no grant must refuse a write, got %#v", got)
	}
	note := &avsproto.Task{Id: "note", Name: "Ping"}
	if got := CoverageRefusal(nil, DeriveWorkflowNeeds(note, nil, scheduleFromTask(note, now), skillSepolia)[skillSepolia]); got != nil {
		t.Fatalf("notification-only must pass, got %#v", got)
	}

	unresolved := skillWriteTask("tmpl", "Template", "{{settings.token}}", data, skillSepolia, 1, 0, 0)
	unresolvedNeed := DeriveWorkflowNeeds(unresolved, nil, scheduleFromTask(unresolved, now), skillSepolia)[skillSepolia]
	grant := usableGrant("01GRANT000000000000000000", later, []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)}, []model.ERC20SpendCap{skillCap(skillUSDC, "100")})
	if unresolvedNeed == nil || !unresolvedNeed.Unresolved {
		t.Fatalf("template target must be unresolved, got %#v", unresolvedNeed)
	}
	if got := CoverageRefusal(grant, unresolvedNeed); got == nil || got.Code != SessionPolicyNotCoveringCode {
		t.Fatalf("unresolved target with a grant must refuse, got %#v", got)
	}
	if got := CoverageRefusal(nil, unresolvedNeed); got == nil {
		t.Fatal("no grant must refuse even when the target is a template")
	}

	unsized := skillWriteTask("max", "Max", skillUSDC, "", skillSepolia, 1, 0, 0)
	unsized.Nodes[0].GetContractWrite().Config.MethodCalls = []*avsproto.ContractWriteNode_MethodCall{{
		MethodName:   "transfer",
		MethodParams: []string{payee.Hex(), "max"},
	}}
	unsizedNeed := DeriveWorkflowNeeds(unsized, nil, scheduleFromTask(unsized, now), skillSepolia)[skillSepolia]
	if unsizedNeed == nil || !unsizedNeed.CapNeedsInput {
		t.Fatalf("max amount must need input, got %#v", unsizedNeed)
	}
	if got := CoverageRefusal(grant, unsizedNeed); got != nil {
		t.Fatalf("unsized amount must not refuse when the action is covered, got %#v", got)
	}

	// A cap of 1 does not cover a 2-unit transfer. Zero is not a valid cap.
	short := usableGrant("01SHORT000000000000000000", later, grant.AllowedActions, []model.ERC20SpendCap{skillCap(skillUSDC, "1")})
	bigger := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(2)), skillSepolia, 1, 0, later)
	biggerNeed := DeriveWorkflowNeeds(bigger, nil, scheduleFromTask(bigger, now), skillSepolia)[skillSepolia]
	if got := CoverageRefusal(short, biggerNeed); got == nil || got.Code != SessionPolicyNotCoveringCode {
		t.Fatalf("cap shortfall must refuse, got %#v", got)
	}

	early := usableGrant(grant.ID, now.Add(time.Hour).UnixMilli(), grant.AllowedActions, grant.ERC20SpendCaps)
	if got := CoverageRefusal(early, need); got == nil || !strings.Contains(got.Detail, "expired") {
		t.Fatalf("expiry shortfall must refuse, got %#v", got)
	}
	open := usableGrant(grant.ID, 0, grant.AllowedActions, grant.ERC20SpendCaps)
	if got := CoverageRefusal(open, need); got != nil {
		t.Fatalf("validUntil 0 is not an expiry shortfall, got %#v", got)
	}
}

func TestDeriveValueZeroIsNotUnsized(t *testing.T) {
	now := skillNow()
	zero := DeriveWorkflowNeeds(skillValueTask("0"), nil, scheduleFromTask(skillValueTask("0"), now), skillSepolia)[skillSepolia]
	if zero == nil || !zero.HasFundMove || zero.CapNeedsInput {
		t.Fatalf("value 0 must move funds without needing a cap, got %#v", zero)
	}
	maxed := DeriveWorkflowNeeds(skillValueTask("max"), nil, scheduleFromTask(skillValueTask("max"), now), skillSepolia)[skillSepolia]
	if maxed == nil || !maxed.CapNeedsInput || !maxed.NativeUnsized {
		t.Fatalf("value max must be unsized native, got %#v", maxed)
	}
}

const (
	skillRouter              = "0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E"
	selectorExactInputSingle = "0x04e45aaf"
)

func exactInputSingleABI(t *testing.T) []*structpb.Value {
	t.Helper()
	entry, err := structpb.NewValue(map[string]any{
		"inputs": []any{
			map[string]any{
				"name": "params",
				"type": "tuple",
				"components": []any{
					map[string]any{"name": "tokenIn", "type": "address"},
					map[string]any{"name": "tokenOut", "type": "address"},
					map[string]any{"name": "fee", "type": "uint24"},
					map[string]any{"name": "recipient", "type": "address"},
					map[string]any{"name": "amountIn", "type": "uint256"},
					map[string]any{"name": "amountOutMinimum", "type": "uint256"},
					map[string]any{"name": "sqrtPriceLimitX96", "type": "uint160"},
				},
			},
		},
		"name":            "exactInputSingle",
		"outputs":         []any{map[string]any{"name": "amountOut", "type": "uint256"}},
		"stateMutability": "payable",
		"type":            "function",
	})
	if err != nil {
		t.Fatal(err)
	}
	return []*structpb.Value{entry}
}

// swapSkillTask is an approve of 1 USDC plus exactInputSingle named in the
// ABI, with no router calldata. That is the Studio swap shape.
func swapSkillTask(t *testing.T, maxExec int64) *avsproto.Task {
	t.Helper()
	usdc := skillUSDC
	return &avsproto.Task{
		Id:           "swap",
		Name:         "Recurring swap",
		MaxExecution: maxExec,
		Nodes: []*avsproto.TaskNode{{
			TaskType: &avsproto.TaskNode_ContractWrite{
				ContractWrite: &avsproto.ContractWriteNode{
					Config: &avsproto.ContractWriteNode_Config{
						ChainId:         skillSepolia,
						ContractAddress: skillRouter,
						ContractAbi:     exactInputSingleABI(t),
						MethodCalls: []*avsproto.ContractWriteNode_MethodCall{
							{
								ContractAddress: &usdc,
								MethodName:      "approve",
								MethodParams:    []string{skillRouter, "1"},
							},
							{MethodName: "exactInputSingle"},
						},
					},
				},
			},
		}},
	}
}

func actionHasSelector(actions []model.AllowedAction, token, selector string) bool {
	want := common.HexToAddress(token)
	selector = normalizeSelector(selector)
	for _, action := range actions {
		if action.Target == nil || *action.Target != want {
			continue
		}
		for _, sel := range action.Selectors {
			if normalizeSelector(sel) == selector {
				return true
			}
		}
	}
	return false
}

func TestSwapMethodNameResolvesRouterSelector(t *testing.T) {
	now := skillNow()
	task := swapSkillTask(t, 12)
	need := DeriveWorkflowNeeds(task, nil, scheduleFromTask(task, now), skillSepolia)[skillSepolia]
	if need == nil || need.CapNeedsInput || need.Unresolved {
		t.Fatalf("swap need = %#v", need)
	}
	if !actionHasSelector(need.Actions, skillUSDC, selectorApprove) {
		t.Fatalf("missing USDC approve: %+v", need.Actions)
	}
	if !actionHasSelector(need.Actions, skillRouter, selectorExactInputSingle) {
		t.Fatalf("missing router exactInputSingle: %+v", need.Actions)
	}
	router := common.HexToAddress(skillRouter)
	for _, cap := range need.Caps {
		if cap.Token != nil && *cap.Token == router {
			t.Fatalf("router must not be an ERC-20 spend cap: %+v", need.Caps)
		}
	}
	if len(need.Caps) != 1 || need.Caps[0].Amount != "12" {
		t.Fatalf("approve cap = %+v, want 1 x 12", need.Caps)
	}

	later := now.Add(30 * 24 * time.Hour).UnixMilli()
	approveOnly := usableGrant("01APPROVE000000000000000", later,
		[]model.AllowedAction{skillAction(skillUSDC, selectorApprove)},
		[]model.ERC20SpendCap{skillCap(skillUSDC, "12")})
	if got := CoverageRefusal(approveOnly, need); got == nil || got.Code != SessionPolicyNotCoveringCode {
		t.Fatalf("a grant without the router must refuse the swap, got %#v", got)
	}
	covered := usableGrant("01SWAP000000000000000000", later,
		[]model.AllowedAction{
			skillAction(skillUSDC, selectorApprove),
			skillAction(skillRouter, selectorExactInputSingle),
		},
		[]model.ERC20SpendCap{skillCap(skillUSDC, "12")})
	if got := CoverageRefusal(covered, need); got != nil {
		t.Fatalf("router plus approve must cover the swap, got %#v", got)
	}

	// A new skill replaces the grant. The running swap's router has to
	// stay, because the replacement is sized from what enabled tasks need.
	perms, changes, err := MergeSkillGrant(covered, PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillWETH, selectorTransfer)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillWETH, "1")},
		ValidUntilMs:   later,
	}, []*avsproto.Task{task}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !actionHasSelector(perms.AllowedActions, skillRouter, selectorExactInputSingle) {
		t.Fatalf("merge dropped the router: %+v", perms.AllowedActions)
	}
	if !actionHasSelector(perms.AllowedActions, skillUSDC, selectorApprove) {
		t.Fatalf("merge dropped the approve: %+v", perms.AllowedActions)
	}
	for _, line := range changes.Summary {
		if strings.Contains(line, "Removed") && strings.Contains(strings.ToLower(line), "04e45aaf") {
			t.Fatalf("summary removed the running swap: %q", line)
		}
	}
}

func TestEthSendCapIsRemainingTimesRuns(t *testing.T) {
	now := skillNow()
	dest := common.HexToAddress("0x0000000000000000000000000000000000000001")
	task := &avsproto.Task{
		Id:           "eth",
		Name:         "Send ETH",
		MaxExecution: 12,
		Nodes: []*avsproto.TaskNode{{
			TaskType: &avsproto.TaskNode_EthTransfer{
				EthTransfer: &avsproto.ETHTransferNode{
					Config: &avsproto.ETHTransferNode_Config{
						Destination: dest.Hex(),
						Amount:      "1000000000000000",
						ChainId:     skillSepolia,
					},
				},
			},
		}},
	}
	need := DeriveWorkflowNeeds(task, nil, scheduleFromTask(task, now), skillSepolia)[skillSepolia]
	if need == nil || need.CapNeedsInput || need.Unresolved || need.NativeSpendCap == nil {
		t.Fatalf("eth need = %#v", need)
	}
	if need.NativeSpendCap.Amount != "12000000000000000" {
		t.Fatalf("native cap = %s, want 1e15 x 12", need.NativeSpendCap.Amount)
	}
	if len(need.NativeRecipients) != 1 || *need.NativeRecipients[0] != dest {
		t.Fatalf("recipients = %+v", need.NativeRecipients)
	}

	perms, _, err := MergeSkillGrant(nil, PolicyAddition{}, []*avsproto.Task{task}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if perms.NativeSpendCap == nil || perms.NativeSpendCap.Amount != "12000000000000000" {
		t.Fatalf("task alone must supply the native total, got %+v", perms.NativeSpendCap)
	}

	withAdd, _, err := MergeSkillGrant(nil, PolicyAddition{
		NativeRecipients: []*common.Address{&dest},
		NativeSpendCap:   &model.NativeSpendCap{Amount: "5"},
	}, []*avsproto.Task{task}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if withAdd.NativeSpendCap == nil || withAdd.NativeSpendCap.Amount != "12000000000000005" {
		t.Fatalf("merged native = %+v", withAdd.NativeSpendCap)
	}

	later := now.Add(30 * 24 * time.Hour).UnixMilli()
	short := usableGrant("01ETH0000000000000000000", later, nil, nil)
	short.NativeRecipients = []*common.Address{&dest}
	short.NativeSpendCap = &model.NativeSpendCap{Amount: "5"}
	if got := CoverageRefusal(short, need); got == nil || !strings.Contains(got.Detail, "native cap") {
		t.Fatalf("native shortfall = %#v", got)
	}
	full := usableGrant(short.ID, later, nil, nil)
	full.NativeRecipients = []*common.Address{&dest}
	full.NativeSpendCap = &model.NativeSpendCap{Amount: "12000000000000000"}
	if got := CoverageRefusal(full, need); got != nil {
		t.Fatalf("full native cap must cover the send, got %#v", got)
	}
}

func TestSpendCapRejectsAboveUint256(t *testing.T) {
	got, err := parseCapAmount(maxUint256.String())
	if err != nil || got.Cmp(maxUint256) != 0 {
		t.Fatalf("MaxUint256 must be a legal cap, got %v %v", got, err)
	}
	over := new(big.Int).Add(maxUint256, big.NewInt(5))
	if _, err := parseCapAmount(over.String()); err == nil || !strings.Contains(err.Error(), "uint256") {
		t.Fatalf("MaxUint256+5 must be rejected, got %v", err)
	}

	now := skillNow()
	router := common.HexToAddress(skillRouter)
	approveMax := func(runs int64) *avsproto.Task {
		return &avsproto.Task{
			Id:           "approve-max",
			Name:         "Approve",
			MaxExecution: runs,
			Nodes: []*avsproto.TaskNode{{
				TaskType: &avsproto.TaskNode_ContractWrite{
					ContractWrite: &avsproto.ContractWriteNode{
						Config: &avsproto.ContractWriteNode_Config{
							ContractAddress: skillUSDC,
							ChainId:         skillSepolia,
							MethodCalls: []*avsproto.ContractWriteNode_MethodCall{{
								MethodName:   "approve",
								MethodParams: []string{router.Hex(), maxUint256.String()},
							}},
						},
					},
				},
			}},
		}
	}
	twelve := DeriveWorkflowNeeds(approveMax(12), nil, scheduleFromTask(approveMax(12), now), skillSepolia)[skillSepolia]
	if twelve == nil || !twelve.CapNeedsInput || len(twelve.Caps) != 0 || len(twelve.Actions) != 1 {
		t.Fatalf("MaxUint256 x 12 must be unsized and keep the action, got %#v", twelve)
	}
	once := DeriveWorkflowNeeds(approveMax(1), nil, scheduleFromTask(approveMax(1), now), skillSepolia)[skillSepolia]
	if once == nil || once.CapNeedsInput || len(once.Caps) != 1 || once.Caps[0].Amount != maxUint256.String() {
		t.Fatalf("MaxUint256 x 1 must fit, got %#v", once)
	}

	unit := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(router, big.NewInt(1)), skillSepolia, 1, 0, 0)
	if _, _, err := MergeSkillGrant(nil, PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, maxUint256.String())},
	}, []*avsproto.Task{unit}, skillSepolia, now, time.Hour); !errors.Is(err, ErrSessionPolicyUnsized) || !strings.Contains(err.Error(), "uint256") {
		t.Fatalf("MaxUint256 plus a running transfer must fail closed, got %v", err)
	}
	if _, _, err := MergeSkillGrant(nil, PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, over.String())},
	}, nil, skillSepolia, now, time.Hour); !errors.Is(err, ErrSessionPolicyUnsized) || !strings.Contains(err.Error(), "uint256") {
		t.Fatalf("a client cap above uint256 must fail closed, got %v", err)
	}

	report := &SessionGrantReport{}
	report.noteNoGrant()
	report.observeCalls([]PlannedCall{{
		Target:   common.HexToAddress(skillUSDC),
		Calldata: common.FromHex(transferCalldata(router, maxUint256)),
	}})
	auth := BuildAuthorization(nil, &WorkflowNeed{HasFundMove: true, Name: "Approve"}, report, SkillSchedule{MaxExecution: 12, Now: now})
	if auth.Status != AuthNoGrant || auth.Required == nil || !auth.Required.CapNeedsInput || len(auth.Required.Caps) != 0 {
		t.Fatalf("observed MaxUint256 x 12 must not become a wrapped cap: %#v", auth)
	}
}

func TestUnknownEndDoesNotShortenWalletExpiry(t *testing.T) {
	now := skillNow()
	oct2 := time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
	oct15 := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	nov1 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	dec28 := time.Date(2026, 12, 28, 0, 0, 0, 0, time.UTC)
	// exactInputSingle only. An approve with no run count is unsized and
	// the merge would fail before the expiry floor.
	running := swapSkillTask(t, 0)
	running.Nodes[0].GetContractWrite().Config.MethodCalls = []*avsproto.ContractWriteNode_MethodCall{
		{MethodName: "exactInputSingle"},
	}
	need := DeriveWorkflowNeeds(running, nil, scheduleFromTask(running, now), skillSepolia)[skillSepolia]
	if need == nil || need.ValidUntilMs != 0 || !actionHasSelector(need.Actions, skillRouter, selectorExactInputSingle) {
		t.Fatalf("unlimited swap need = %#v", need)
	}

	current := usableGrant("01SWAP000000000000000000", nov1.UnixMilli(),
		[]model.AllowedAction{skillAction(skillRouter, selectorExactInputSingle)}, nil)
	addition := PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "12")},
		ValidUntilMs:   oct2.UnixMilli(),
	}
	perms, changes, err := MergeSkillGrant(current, addition, []*avsproto.Task{running}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if perms.ValidUntilMs != nov1.UnixMilli() {
		t.Fatalf("wallet expiry = %d, want Nov 1 (%d)", perms.ValidUntilMs, nov1.UnixMilli())
	}
	for _, line := range changes.Summary {
		if strings.Contains(line, "was Nov") || strings.Contains(line, "Oct 2") {
			t.Fatalf("summary shortened the wallet: %q", line)
		}
	}

	later := addition
	later.ValidUntilMs = dec28.UnixMilli()
	extended, _, err := MergeSkillGrant(current, later, []*avsproto.Task{running}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if extended.ValidUntilMs != dec28.UnixMilli() {
		t.Fatalf("a later skill must still extend, got %d", extended.ValidUntilMs)
	}

	open := usableGrant(current.ID, 0, current.AllowedActions, nil)
	unfloored, _, err := MergeSkillGrant(open, addition, []*avsproto.Task{running}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if unfloored.ValidUntilMs != oct2.UnixMilli() {
		t.Fatalf("a current expiry of 0 must not act as forever, got %d", unfloored.ValidUntilMs)
	}

	known := swapSkillTask(t, 1)
	known.Nodes[0].GetContractWrite().Config.MethodCalls = []*avsproto.ContractWriteNode_MethodCall{
		{MethodName: "exactInputSingle"},
	}
	known.ExpiredAt = oct15.UnixMilli()
	shortened, _, err := MergeSkillGrant(current, addition, []*avsproto.Task{known}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if shortened.ValidUntilMs != oct15.UnixMilli() {
		t.Fatalf("a known earlier end stays the latest end the tasks need, got %d", shortened.ValidUntilMs)
	}
}

func TestOrderedSessionLocksSortByShardIndex(t *testing.T) {
	owner := common.HexToAddress("0x00000000000000000000000000000000000000aa")
	const base = int64(8453)
	type shardPair struct{ base, sep uint32 }
	seen := map[shardPair]common.Address{}
	var runnerA, runnerB common.Address
	found := false
	for i := 1; i <= 20000 && !found; i++ {
		runner := common.BigToAddress(big.NewInt(int64(i)))
		b := sessionAuthorityShard(base, owner, runner)
		s := sessionAuthorityShard(skillSepolia, owner, runner)
		if b == s {
			continue
		}
		if other, ok := seen[shardPair{base: s, sep: b}]; ok {
			runnerA, runnerB = other, runner
			found = true
			break
		}
		seen[shardPair{base: b, sep: s}] = runner
	}
	if !found {
		t.Fatal("expected two runners whose Base/Sepolia shards are the same pair in opposite chain order")
	}
	aChainBase := sessionAuthorityShard(base, owner, runnerA)
	aChainSep := sessionAuthorityShard(skillSepolia, owner, runnerA)
	bChainBase := sessionAuthorityShard(base, owner, runnerB)
	bChainSep := sessionAuthorityShard(skillSepolia, owner, runnerB)
	if aChainBase != bChainSep || aChainSep != bChainBase {
		t.Fatalf("pair did not cross: A %d/%d B %d/%d", aChainBase, aChainSep, bChainBase, bChainSep)
	}
	// Pass chains in chain-id order. That order deadlocks these two
	// runners; shard order must lock the shared mutexes the same way.
	aLocks := orderedSessionLocks([]int64{base, skillSepolia}, owner, runnerA)
	bLocks := orderedSessionLocks([]int64{base, skillSepolia}, owner, runnerB)
	if len(aLocks) != 2 || len(bLocks) != 2 {
		t.Fatalf("locks A %d B %d", len(aLocks), len(bLocks))
	}
	if aLocks[0] != bLocks[0] || aLocks[1] != bLocks[1] {
		t.Fatalf("shared shards locked in different orders: A %d,%d B %d,%d",
			shardIndex(aLocks[0]), shardIndex(aLocks[1]), shardIndex(bLocks[0]), shardIndex(bLocks[1]))
	}
	if shardIndex(aLocks[0]) > shardIndex(aLocks[1]) {
		t.Fatalf("shard order %d then %d", shardIndex(aLocks[0]), shardIndex(aLocks[1]))
	}
	same := orderedSessionLocks([]int64{skillSepolia, skillSepolia}, owner, runnerA)
	if len(same) != 1 {
		t.Fatalf("a repeated chain must lock its shard once, got %d", len(same))
	}
}

func shardIndex(mu *sync.RWMutex) int {
	for i := range sessionAuthorityLocks {
		if mu == &sessionAuthorityLocks[i] {
			return i
		}
	}
	return -1
}

// weeklyPayTask is the Weekly USDC Pay shape: a loop whose runner transfers
// {{settings.token_amount.amount}} of {{settings.token_amount.address}}.
func weeklyPayTask(t *testing.T, addressExpr string, amount any, recipients []any) *avsproto.Task {
	t.Helper()
	settings, err := structpb.NewValue(map[string]any{
		"recipients": recipients,
		"token_amount": map[string]any{
			"address": skillUSDC,
			"amount":  amount,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &avsproto.Task{
		Id:           "weekly",
		Name:         "Weekly USDC Pay",
		MaxExecution: 12,
		InputVariables: map[string]*structpb.Value{
			"settings": settings,
		},
		Nodes: []*avsproto.TaskNode{{
			TaskType: &avsproto.TaskNode_Loop{
				Loop: &avsproto.LoopNode{
					Config: &avsproto.LoopNode_Config{InputVariable: "{{settings.recipients}}"},
					Runner: &avsproto.LoopNode_ContractWrite{
						ContractWrite: &avsproto.ContractWriteNode{
							Config: &avsproto.ContractWriteNode_Config{
								ChainId:         skillSepolia,
								ContractAddress: addressExpr,
								MethodCalls: []*avsproto.ContractWriteNode_MethodCall{{
									MethodName:   "transfer",
									MethodParams: []string{"{{value}}", "{{settings.token_amount.amount}}"},
								}},
							},
						},
					},
				},
			},
		}},
	}
}

func TestWeeklyPayNestedSettingsDerivesTransferAndCap(t *testing.T) {
	now := skillNow()
	one := []any{"0x0000000000000000000000000000000000000001"}
	task := weeklyPayTask(t, "{{settings.token_amount.address}}", "1000000", one)
	need := DeriveWorkflowNeeds(task, nil, scheduleFromTask(task, now), skillSepolia)[skillSepolia]
	if need == nil || need.CapNeedsInput || len(need.Actions) != 1 || len(need.Caps) != 1 {
		t.Fatalf("nested transfer need = %#v", need)
	}
	usdc := common.HexToAddress(skillUSDC)
	if *need.Actions[0].Target != usdc || need.Actions[0].Selectors[0] != selectorTransfer {
		t.Fatalf("action = %+v", need.Actions[0])
	}
	if need.Caps[0].Amount != "12000000" {
		t.Fatalf("cap = %s, want 1000000 x 12 runs", need.Caps[0].Amount)
	}

	two := DeriveWorkflowNeeds(task, map[string]any{
		"recipients": []any{
			"0x0000000000000000000000000000000000000001",
			"0x0000000000000000000000000000000000000002",
		},
		"token_amount": map[string]any{"address": skillUSDC, "amount": "1000000"},
	}, scheduleFromTask(task, now), skillSepolia)[skillSepolia]
	if two == nil || len(two.Caps) != 1 || two.Caps[0].Amount != "24000000" {
		t.Fatalf("two recipients cap = %#v", two)
	}

	numeric := DeriveWorkflowNeeds(weeklyPayTask(t, "${settings.token_amount.address}", float64(1000000), one), nil, scheduleFromTask(task, now), skillSepolia)[skillSepolia]
	if numeric == nil || numeric.CapNeedsInput || len(numeric.Actions) != 1 || numeric.Caps[0].Amount != "12000000" {
		t.Fatalf("numeric amount and ${} path = %#v", numeric)
	}

	huge := DeriveWorkflowNeeds(weeklyPayTask(t, "{{settings.token_amount.address}}", float64(1<<54), one), nil, scheduleFromTask(task, now), skillSepolia)[skillSepolia]
	if huge == nil || !huge.CapNeedsInput || len(huge.Caps) != 0 || len(huge.Actions) != 1 {
		t.Fatalf("an inexact JSON amount must keep the action and not invent a cap, got %#v", huge)
	}

	loopValue := DeriveWorkflowNeeds(weeklyPayTask(t, "{{value}}", "1000000", one), nil, scheduleFromTask(task, now), skillSepolia)[skillSepolia]
	if loopValue == nil || !loopValue.HasFundMove || len(loopValue.Actions) != 0 {
		t.Fatalf("{{value}} must not become an allowlist target, got %#v", loopValue)
	}

	later := now.Add(30 * 24 * time.Hour).UnixMilli()
	other := usableGrant("01OTHER000000000000000000", later, []model.AllowedAction{skillAction(skillWETH, selectorApprove)}, []model.ERC20SpendCap{skillCap(skillWETH, "1")})
	if got := CoverageRefusal(other, need); got == nil || got.Code != SessionPolicyNotCoveringCode {
		t.Fatalf("a resolved transfer outside the grant must refuse, got %#v", got)
	}
	covered := usableGrant("01COVERED0000000000000000", later, []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)}, []model.ERC20SpendCap{skillCap(skillUSDC, "12000000")})
	if got := CoverageRefusal(covered, need); got != nil {
		t.Fatalf("a grant of the derived transfer must cover it, got %#v", got)
	}
	if loopValue == nil || !loopValue.Unresolved {
		t.Fatalf("{{value}} must stay unresolved, got %#v", loopValue)
	}
	if got := CoverageRefusal(covered, loopValue); got == nil {
		t.Fatal("an unresolved loop value must not be treated as covered")
	}
	if _, _, err := MergeSkillGrant(nil, PolicyAddition{}, []*avsproto.Task{weeklyPayTask(t, "{{value}}", "1000000", one)}, skillSepolia, now, time.Hour); !errors.Is(err, ErrSessionPolicyUnsized) {
		t.Fatalf("an unresolved running task must fail the merge closed, got %v", err)
	}

	perms, _, err := MergeSkillGrant(nil, PolicyAddition{}, []*avsproto.Task{task}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(perms.AllowedActions) != 1 || perms.AllowedActions[0].Selectors[0] != selectorTransfer {
		t.Fatalf("merged actions = %+v", perms.AllowedActions)
	}
	if len(perms.SpendCaps) != 1 || perms.SpendCaps[0].Amount != "12000000" {
		t.Fatalf("merged cap = %+v", perms.SpendCaps)
	}
}

func TestReportModeRequiredIncludesObservedTransfer(t *testing.T) {
	now := skillNow()
	usdc := common.HexToAddress(skillUSDC)
	router := common.HexToAddress("0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E")
	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
	// Static walk missed the dotted target, which is the shape Studio hit:
	// a fund-moving need with a validUntil and no action.
	missed := &WorkflowNeed{HasFundMove: true, Name: "Weekly USDC Pay", ValidUntilMs: 1798324821519, CapNeedsInput: true}
	report := &SessionGrantReport{}
	report.noteNoGrant()
	report.observeCalls([]PlannedCall{
		{Target: usdc, Selector: selectorTransfer, Calldata: common.FromHex(transferCalldata(payee, big.NewInt(1_000_000)))},
		{Target: router, Selector: "0x04e45aaf", Label: "exactInputSingle", Calldata: common.FromHex("0x04e45aaf")},
	})
	got := BuildAuthorization(nil, missed, report, SkillSchedule{MaxExecution: 12, Now: now})
	if got.Status != AuthNoGrant || got.Detail != "no usable grant" || got.Required == nil {
		t.Fatalf("status = %#v", got)
	}
	req := got.Required
	if req.CapNeedsInput || len(req.Actions) != 1 || len(req.Caps) != 1 {
		t.Fatalf("required = %#v", req)
	}
	if *req.Actions[0].Target != usdc || req.Actions[0].Selectors[0] != selectorTransfer {
		t.Fatalf("action = %+v", req.Actions[0])
	}
	if req.Caps[0].Amount != "12000000" || req.ValidUntilMs != missed.ValidUntilMs {
		t.Fatalf("cap = %+v validUntil = %d", req.Caps, req.ValidUntilMs)
	}

	// The static walk already sized this run. The observed spend must not be added again.
	sized := &WorkflowNeed{
		HasFundMove: true,
		Actions:     []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		Caps:        []model.ERC20SpendCap{skillCap(skillUSDC, "12000000")},
	}
	again := BuildAuthorization(nil, sized, report, SkillSchedule{MaxExecution: 12, Now: now})
	if again.Required == nil || len(again.Required.Caps) != 1 || again.Required.Caps[0].Amount != "12000000" {
		t.Fatalf("observed spend doubled the static cap: %#v", again.Required)
	}
	if len(again.Required.Actions) != 1 {
		t.Fatalf("router call leaked into actions: %+v", again.Required.Actions)
	}
}

func TestClassifyRunnerCoverageDrop(t *testing.T) {
	now := skillNow()
	later := now.Add(30 * 24 * time.Hour).UnixMilli()
	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
	covered := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(1)), skillSepolia, 1, 0, later)
	other := skillWriteTask("swap", "Swap", skillWETH, transferCalldata(payee, big.NewInt(1)), skillSepolia, 1, 0, later)
	in := SessionPolicyInput{
		ChainID: skillSepolia,
		Permissions: SessionPermissions{
			AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
			SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "100")},
			ValidUntilMs:   later,
		},
	}
	if _, err := classifyRunnerCoverage(in, []*avsproto.Task{covered, other}, now); !errors.Is(err, ErrSessionPolicyNotCovering) {
		t.Fatalf("uncovered swap must 409, got %v", err)
	}
	var conflict *PolicyConflictError
	_, err := classifyRunnerCoverage(in, []*avsproto.Task{covered, other}, now)
	if !errors.As(err, &conflict) || len(conflict.AffectedTaskIDs) != 1 || conflict.AffectedTaskIDs[0] != "swap" {
		t.Fatalf("affected = %#v", err)
	}
	in.DropTaskIDs = []string{"swap"}
	dropped, err := classifyRunnerCoverage(in, []*avsproto.Task{covered, other, {Id: "note", Name: "Ping"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 1 || dropped[0] != "swap" {
		t.Fatalf("dropped = %#v", dropped)
	}
}

func TestPolicyIDMatchesEmptyMeansNoGrant(t *testing.T) {
	if !policyIDMatches("", nil) {
		t.Fatal("empty base matches no current grant")
	}
	current := usableGrant("01ABC", 1, nil, nil)
	if policyIDMatches("", current) {
		t.Fatal("empty base must not match a current grant")
	}
	if !policyIDMatches("01abc", current) {
		t.Fatal("ids compare case-insensitively")
	}
	changed := newBaseChanged(current)
	if changed.Code != SessionPolicyBaseChangedCode || changed.PolicyID != current.ID || !errors.Is(changed, ErrSessionPolicyBaseChanged) {
		t.Fatalf("base changed = %#v", changed)
	}
}

func TestBuildAuthorizationStatuses(t *testing.T) {
	if got := BuildAuthorization(nil, &WorkflowNeed{}, nil, SkillSchedule{}); got.Status != AuthCovered || got.Required != nil {
		t.Fatalf("no fund move: %#v", got)
	}
	fund := &WorkflowNeed{HasFundMove: true, Name: "Pay"}
	if got := BuildAuthorization(nil, fund, nil, SkillSchedule{}); got.Status != AuthNoGrant || got.Required == nil {
		t.Fatalf("no grant: %#v", got)
	}
	need := &WorkflowNeed{
		HasFundMove:  true,
		Actions:      []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		Caps:         []model.ERC20SpendCap{skillCap(skillUSDC, "10")},
		ValidUntilMs: 200,
	}
	other := usableGrant("01G", 500, []model.AllowedAction{skillAction(skillWETH, selectorApprove)}, nil)
	if got := BuildAuthorization(other, need, nil, SkillSchedule{}); got.Status != AuthNotCovered || len(got.Missing) != 1 {
		t.Fatalf("missing action: %#v", got)
	}
	low := usableGrant("01G", 500, need.Actions, []model.ERC20SpendCap{skillCap(skillUSDC, "1")})
	if got := BuildAuthorization(low, need, nil, SkillSchedule{}); got.Status != AuthCapTooLow {
		t.Fatalf("cap: %#v", got)
	}
	early := usableGrant("01G", 100, need.Actions, []model.ERC20SpendCap{skillCap(skillUSDC, "10")})
	if got := BuildAuthorization(early, need, nil, SkillSchedule{}); got.Status != AuthExpiresTooSoon {
		t.Fatalf("expiry: %#v", got)
	}
	open := usableGrant("01G", 0, need.Actions, []model.ERC20SpendCap{skillCap(skillUSDC, "10")})
	if got := BuildAuthorization(open, need, nil, SkillSchedule{}); got.Status != AuthCovered {
		t.Fatalf("zero expiry must not be too soon: %#v", got)
	}
	unsized := *need
	unsized.Caps = nil
	unsized.CapNeedsInput = true
	if got := BuildAuthorization(open, &unsized, nil, SkillSchedule{}); got.Status != AuthCapNeedsInput || strings.Contains(got.Detail, "could not be resolved") {
		t.Fatalf("needs input: %#v", got)
	}
	unresolved := &WorkflowNeed{HasFundMove: true, Unresolved: true, Name: "Template"}
	if got := BuildAuthorization(nil, unresolved, nil, SkillSchedule{}); got.Status != AuthNoGrant {
		t.Fatalf("no grant still wins when the target is unresolved: %#v", got)
	}
	if got := BuildAuthorization(open, unresolved, nil, SkillSchedule{}); got.Status != AuthCapNeedsInput || got.Detail != "a fund-moving target could not be resolved" {
		t.Fatalf("unresolved target with a grant: %#v", got)
	}
}

func TestDeployCheckFlagOffIsNoop(t *testing.T) {
	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
	write := &model.Workflow{Task: skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(1)), skillSepolia, 1, 0, 0)}
	if _, err := (&Engine{}).enforceSessionPolicyDeployCheck(nil, write); err != nil {
		t.Fatal(err)
	}
	off := &Engine{config: &config.Config{}}
	if _, err := off.enforceSessionPolicyDeployCheck(nil, write); err != nil {
		t.Fatal(err)
	}
	on := &Engine{
		config:            &config.Config{SessionPolicyDeployCheck: true},
		smartWalletConfig: &config.SmartWalletConfig{ChainID: skillSepolia},
	}
	if _, err := on.enforceSessionPolicyDeployCheck(nil, &model.Workflow{Task: &avsproto.Task{Id: "note", Name: "Ping"}}); err != nil {
		t.Fatal(err)
	}
	_, err := on.enforceSessionPolicyDeployCheck(nil, write)
	if !errors.Is(err, ErrSessionPolicyNotCovering) {
		t.Fatalf("write without a runner must fail closed, got %v", err)
	}
	var conflict *PolicyConflictError
	if errors.As(err, &conflict) {
		t.Fatal("missing runner is a wrapped sentinel, not a structured conflict")
	}
}

func TestDeployCheckMissingChainConfigFailsClosed(t *testing.T) {
	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
	runner := common.HexToAddress("0x0000000000000000000000000000000000000002")
	owner := common.HexToAddress("0x0000000000000000000000000000000000000003")
	write := &model.Workflow{Task: skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(1)), skillSepolia, 1, 0, 0)}
	write.SmartWalletAddress = runner.Hex()
	on := &Engine{
		config: &config.Config{SessionPolicyDeployCheck: true},
		chainConfigs: map[int64]*config.ChainConfig{
			skillSepolia: {},
		},
	}
	_, err := on.enforceSessionPolicyDeployCheck(&model.User{Address: owner}, write)
	if !errors.Is(err, ErrSessionPolicyNotCovering) || !strings.Contains(err.Error(), "no smart wallet config") {
		t.Fatalf("missing chain config must fail closed, got %v", err)
	}
	mu := sessionAuthorityLock(skillSepolia, owner, runner)
	if !mu.TryLock() {
		t.Fatal("a missing chain config must not leave the runner lock held")
	}
	mu.Unlock()
}

func TestDeployCheckHoldsLockUntilReleaseAndNamesTheTask(t *testing.T) {
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })

	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
	runner := common.HexToAddress("0x0000000000000000000000000000000000000002")
	owner := common.HexToAddress("0x0000000000000000000000000000000000000003")
	write := &model.Workflow{Task: skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(1)), skillSepolia, 1, 0, 0)}
	write.SmartWalletAddress = runner.Hex()
	on := &Engine{
		db:                db,
		config:            &config.Config{SessionPolicyDeployCheck: true},
		smartWalletConfig: &config.SmartWalletConfig{ChainID: skillSepolia},
	}
	user := &model.User{Address: owner}

	_, err := on.enforceSessionPolicyDeployCheck(user, write)
	var conflict *PolicyConflictError
	if !errors.As(err, &conflict) || len(conflict.AffectedTaskIDs) != 1 || conflict.AffectedTaskIDs[0] != "pay" {
		t.Fatalf("uncovered task id: %v", err)
	}
	mu := sessionAuthorityLock(skillSepolia, owner, runner)
	if !mu.TryLock() {
		t.Fatal("a refusal must release the runner lock")
	}
	mu.Unlock()

	usdc := common.HexToAddress(skillUSDC)
	policy := usableGrant("01coveredgrantaaaaaaaaaa", 0, []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)}, []model.ERC20SpendCap{skillCap(skillUSDC, "10")})
	policy.Owner = &owner
	policy.Runner = &runner
	policy.ChainID = skillSepolia
	policy.EntityID = 1
	policy.SessionSigner = &usdc
	policy.Grant = &model.SessionGrantAuthorization{
		InstallCall:    []byte{0x1b, 0xbf, 0x56, 0x4c, 0x01},
		CarrierNonce:   big.NewInt(1),
		Deadline:       1785541743,
		OwnerSignature: make([]byte, 65),
	}
	if err := StoreSessionPolicy(db, policy); err != nil {
		t.Fatal(err)
	}
	unlock, err := on.enforceSessionPolicyDeployCheck(user, write)
	if err != nil {
		t.Fatal(err)
	}
	if mu.TryLock() {
		mu.Unlock()
		unlock()
		t.Fatal("a passing deploy check must hold the runner lock until unlock")
	}
	unlock()
	if !mu.TryLock() {
		t.Fatal("unlock must release the runner lock")
	}
	mu.Unlock()
	unlock()
}

func TestDeployCheckTwoChainsReleases(t *testing.T) {
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })

	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
	runner := common.HexToAddress("0x0000000000000000000000000000000000000004")
	owner := common.HexToAddress("0x0000000000000000000000000000000000000005")
	task := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(1)), skillSepolia, 1, 0, 0)
	other := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(1)), 1, 1, 0, 0)
	task.Nodes = append(task.Nodes, other.Nodes...)
	write := &model.Workflow{Task: task}
	write.SmartWalletAddress = runner.Hex()
	on := &Engine{
		db:                db,
		config:            &config.Config{SessionPolicyDeployCheck: true},
		smartWalletConfig: &config.SmartWalletConfig{ChainID: skillSepolia},
	}
	done := make(chan error, 1)
	go func() {
		_, err := on.enforceSessionPolicyDeployCheck(&model.User{Address: owner}, write)
		done <- err
	}()
	select {
	case err := <-done:
		var conflict *PolicyConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("two uncovered chains must refuse, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("two-chain deploy check deadlocked on a shared lock shard")
	}
}

func TestAmbiguousLegacyRepairOnlyWhenBaseOmitted(t *testing.T) {
	err := &sessionPolicyAmbiguousError{wallet: common.Address{1}, ids: [2]string{"a", "b"}}
	if !errors.Is(err, ErrSessionPolicyAmbiguous) {
		t.Fatal(err)
	}
	if !ambiguousLegacyRepair(nil, err) {
		t.Fatal("an omitted basePolicyId repairs a stacked grant")
	}
	empty := ""
	if ambiguousLegacyRepair(&empty, err) {
		t.Fatal("an empty basePolicyId is present and must not repair a stacked grant")
	}
	id := "01coveredgrantaaaaaaaaaa"
	if ambiguousLegacyRepair(&id, err) {
		t.Fatal("a named basePolicyId must not repair a stacked grant")
	}
	if ambiguousLegacyRepair(nil, errors.New("db down")) {
		t.Fatal("a storage error is not a stacked-grant repair")
	}
}

func TestExpiresInSecondsRejectsOverflow(t *testing.T) {
	engine, _, _, owner, wallet := newPolicyTestEngine(t)
	user := &model.User{Address: owner}
	_, err := engine.PrepareSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain,
		AgentLabel: "bot", ExpiresInSeconds: MaxSessionExpiresInSeconds + 1,
		Permissions: testPermissions(),
	})
	if err == nil || !strings.Contains(err.Error(), "expiresInSeconds") {
		t.Fatalf("overflow must be rejected, got %v", err)
	}
}

func TestPreflightReportModeRecordsMissWithoutFailingTheStep(t *testing.T) {
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })

	owner := common.HexToAddress("0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557")
	wallet := common.HexToAddress("0x209eb31c199bEB4c386eF83CF442DE1a00667a1F")
	signer := common.HexToAddress("0x82F2Dd9a552a69f2ceD7Ff2D05c43aB8430158FB")
	usdc := common.HexToAddress(skillUSDC)
	router := common.HexToAddress("0x3bFA4769FB09eefC5a80d6E87c3B9C650f7Ae48E")
	policy := &model.SessionPolicy{
		ID: "01reportmodeaaaaaaaaaaaaaa", Owner: &owner, Runner: &wallet,
		ChainID: skillSepolia, EntityID: 1, SessionSigner: &signer,
		Status: model.SessionPolicyPending,
		AllowedActions: []model.AllowedAction{
			{Target: &usdc, Selectors: []string{selectorApprove}},
		},
		Grant: &model.SessionGrantAuthorization{
			InstallCall:    []byte{0x1b, 0xbf, 0x56, 0x4c, 0x01},
			CarrierNonce:   big.NewInt(1),
			Deadline:       1785541743,
			OwnerSignature: make([]byte, 65),
		},
	}
	if err := StoreSessionPolicy(db, policy); err != nil {
		t.Fatal(err)
	}
	planned := []PlannedCall{{Target: router, Selector: "0x04e45aaf", Label: "exactInputSingle"}}
	newProc := func(report *SessionGrantReport) *ContractWriteProcessor {
		return &ContractWriteProcessor{
			CommonProcessor: &CommonProcessor{vm: &VM{
				db: db, TaskOwner: owner, mu: new(sync.Mutex),
				vars:               map[string]any{"aa_sender": wallet.Hex()},
				sessionGrantReport: report,
			}},
			smartWalletConfig: &config.SmartWalletConfig{ChainID: skillSepolia},
			owner:             owner,
		}
	}
	if msg := newProc(nil).preflightSessionGrantCoverage(planned); !strings.HasPrefix(msg, "SESSION_POLICY_TARGET_NOT_ALLOWED:") {
		t.Fatalf("enforce mode must still fail the step, got %q", msg)
	}
	report := &SessionGrantReport{}
	if msg := newProc(report).preflightSessionGrantCoverage(planned); msg != "" {
		t.Fatalf("report mode must not fail the step, got %q", msg)
	}
	if !report.SawWrite || len(report.Missing) != 1 || report.Missing[0].Target != router {
		t.Fatalf("report = saw %v missing %+v", report.SawWrite, report.Missing)
	}
}
