package taskengine

import (
	"errors"
	"fmt"
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
	// 7 runs left (10 - 3) at 1 unit. The chain has 8 of the stored 24 left,
	// so the merge keeps max(8, 7) plus the addition of 12. WETH is spent
	// and is dropped rather than installed at 0. The stored 24 is not the
	// "was" amount.
	task := skillWriteTask("swap-1", "Weekly swap", skillUSDC, transferCalldata(payee, big.NewInt(1)), skillSepolia, 10, 3, dec28.UnixMilli())
	current := usableGrant("01OLDGRANT000000000000000", nov30.UnixMilli(), []model.AllowedAction{
		skillAction(skillUSDC, selectorTransfer),
		skillAction(skillWETH, selectorApprove),
	}, []model.ERC20SpendCap{skillCap(skillUSDC, "24"), skillCap(skillWETH, "5")})
	usdc := common.HexToAddress(skillUSDC)
	weth := common.HexToAddress(skillWETH)
	remainder := &GrantRemainder{ERC20: map[common.Address]*big.Int{
		usdc: big.NewInt(8),
		weth: big.NewInt(0),
	}}

	perms, changes, err := MergeSkillGrantWithRemainder(current, remainder, PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "12")},
		ValidUntilMs:   time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).UnixMilli(),
	}, []*avsproto.Task{task}, skillSepolia, now, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := perms.SpendCaps; len(got) != 1 || got[0].Amount != "20" {
		t.Fatalf("merged cap = %+v, want USDC 20", got)
	}
	if perms.SpendCap == nil || perms.SpendCap.Amount != "20" {
		t.Fatalf("alias cap = %+v, want 20", perms.SpendCap)
	}
	if perms.ValidUntilMs != dec28.UnixMilli() {
		t.Fatalf("validUntil = %d, want Dec 28", perms.ValidUntilMs)
	}
	if changes.BasePolicyID != current.ID {
		t.Fatalf("base = %q", changes.BasePolicyID)
	}
	wantExpiry := "Weekly swap: until Dec 28, was Nov 30"
	if len(changes.Summary) == 0 || changes.Summary[0] != wantExpiry {
		t.Fatalf("summary = %#v", changes.Summary)
	}
	foundCap := false
	for _, line := range changes.Summary {
		if strings.Contains(line, "was 24") || strings.Contains(line, "was 19") || strings.Contains(line, ": 19") {
			t.Fatalf("cap reused the stored total or the pre-carry sum: %q", line)
		}
		if line == "Cap "+usdc.Hex()+": 20 (was 8)" {
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
			t.Fatal("merged grant kept a spent WETH action")
		}
	}
	for _, cap := range perms.SpendCaps {
		if cap.Amount == "0" {
			t.Fatal("a zero cap was installed")
		}
	}
}

func TestMergeSkillGrantUnappliedCarriesStoredCap(t *testing.T) {
	now := skillNow()
	dec28 := time.Date(2026, 12, 28, 0, 0, 0, 0, time.UTC)
	nov30 := time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC)
	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
	// The grant is signed but not applied, so nothing can have been spent.
	// A nil remainder uses the stored caps: max(24, 7) + 12, and the WETH
	// approve stays at its stored 5.
	task := skillWriteTask("swap-1", "Weekly swap", skillUSDC, transferCalldata(payee, big.NewInt(1)), skillSepolia, 10, 3, dec28.UnixMilli())
	current := usableGrant("01OLDGRANT000000000000000", nov30.UnixMilli(), []model.AllowedAction{
		skillAction(skillUSDC, selectorTransfer),
		skillAction(skillWETH, selectorApprove),
	}, []model.ERC20SpendCap{skillCap(skillUSDC, "24"), skillCap(skillWETH, "5")})
	if current.Grant.Applied() {
		t.Fatal("precondition: usableGrant is not applied")
	}

	perms, changes, err := MergeSkillGrant(current, PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "12")},
		ValidUntilMs:   time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).UnixMilli(),
	}, []*avsproto.Task{task}, skillSepolia, now, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	usdc := common.HexToAddress(skillUSDC)
	weth := common.HexToAddress(skillWETH)
	if got, ok := spendCapAmount(perms, usdc); !ok || got != "36" {
		t.Fatalf("USDC cap = %q, want 36", got)
	}
	if got, ok := spendCapAmount(perms, weth); !ok || got != "5" {
		t.Fatalf("WETH cap = %q, want the stored 5", got)
	}
	if !actionHasSelector(perms.AllowedActions, skillWETH, selectorApprove) {
		t.Fatal("unapplied WETH approve was dropped")
	}
	found := false
	for _, line := range changes.Summary {
		if line == "Cap "+usdc.Hex()+": 36 (was 24)" {
			found = true
		}
	}
	if !found {
		t.Fatalf("summary = %#v", changes.Summary)
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

func TestMergeSkillGrantUnsizedNamedSpend(t *testing.T) {
	// Any named transfer with no derived total, not only split and batch.
	// A cap on a different token does not size it. A positive cap on the
	// same token is the ceiling and is the only amount merged.
	task := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(common.HexToAddress("0x0000000000000000000000000000000000000001"), big.NewInt(1)), skillSepolia, 0, 0, 0)
	now := skillNow()
	_, _, err := MergeSkillGrant(nil, PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorApprove)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillWETH, "1")},
	}, []*avsproto.Task{task}, skillSepolia, now, time.Hour)
	if !errors.Is(err, ErrSessionPolicyUnsized) {
		t.Fatalf("unlimited transfer must fail closed, got %v", err)
	}
	// The same token's positive addition is the ceiling. The task adds the
	// transfer and no derived amount.
	perms, _, err := MergeSkillGrant(nil, PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "40")},
	}, []*avsproto.Task{task}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := spendCapAmount(perms, common.HexToAddress(skillUSDC)); !ok || got != "40" {
		t.Fatalf("known-token ceiling = %q, want the addition only", got)
	}
	if !actionHasSelector(perms.AllowedActions, skillUSDC, selectorTransfer) {
		t.Fatalf("actions = %+v", perms.AllowedActions)
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
	if got := CoverageRefusal(grant, unresolvedNeed); got == nil || got.Code != SessionPolicyTargetUnresolvedCode || !errors.Is(got, ErrSessionPolicyNotCovering) {
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
	// Submit and carry call CoverageRefusal. An already-closed grant with
	// no workflow end must still count, or a re-grant would lose the entity.
	noEnd := skillManualTransfer("manual", 0)
	noEndNeed := DeriveWorkflowNeeds(noEnd, nil, scheduleFromTask(noEnd, now), skillSepolia)[skillSepolia]
	if noEndNeed == nil || !noEndNeed.HasFundMove || noEndNeed.ValidUntilMs != 0 {
		t.Fatalf("manual transfer with no end = %#v", noEndNeed)
	}
	closed := usableGrant(grant.ID, now.Add(-time.Hour).UnixMilli(), grant.AllowedActions, grant.ERC20SpendCaps)
	if !closed.Usable() || !closed.Expired(now) {
		t.Fatal("the closed grant must stay usable and count as expired")
	}
	if got := CoverageRefusal(closed, noEndNeed); got != nil {
		t.Fatalf("an expired grant must still count for submit and carry, got %#v", got)
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
	shortened, changes, err := MergeSkillGrant(current, addition, []*avsproto.Task{known}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	// The wallet expiry is the latest of the current grant (Nov 1), the
	// addition (Oct 2), and the task's known end (Oct 15). A known earlier
	// end must not pull the wallet back.
	if shortened.ValidUntilMs != nov1.UnixMilli() {
		t.Fatalf("a known earlier end must not shorten the wallet, got %d want Nov 1 %d", shortened.ValidUntilMs, nov1.UnixMilli())
	}
	for _, line := range changes.Summary {
		if strings.Contains(line, "Oct 15") || strings.Contains(line, "Oct 2") {
			t.Fatalf("summary shortened the wallet: %q", line)
		}
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

	// {{value}} over a node output is not a settings list. The token stays
	// unread, so the walk must not invent an allowlist target.
	nodeOutput := weeklyPayTask(t, "{{value}}", "1000000", one)
	nodeOutput.GetNodes()[0].GetLoop().GetConfig().InputVariable = "{{split1.data}}"
	loopValue := DeriveWorkflowNeeds(nodeOutput, nil, scheduleFromTask(nodeOutput, now), skillSepolia)[skillSepolia]
	if loopValue == nil || !loopValue.HasFundMove || len(loopValue.Actions) != 0 {
		t.Fatalf("{{value}} over a node output must not become an allowlist target, got %#v", loopValue)
	}

	// The same {{value}} over a settings list of token addresses is the
	// contract the loop calls. Each address is an allowlist target, and the
	// list length is applied once (1000000 x 12, not x 12 x 2).
	byValue := DeriveWorkflowNeeds(weeklyPayTask(t, "{{value}}", "1000000", []any{skillUSDC, skillWETH}), nil, scheduleFromTask(task, now), skillSepolia)[skillSepolia]
	weth := common.HexToAddress(skillWETH)
	if byValue == nil || byValue.Unresolved || byValue.CapNeedsInput || len(byValue.Actions) != 2 || len(byValue.Caps) != 2 {
		t.Fatalf("settings-list {{value}} contract = %#v", byValue)
	}
	if *byValue.Actions[0].Target != usdc || byValue.Actions[0].Selectors[0] != selectorTransfer {
		t.Fatalf("first token action = %+v", byValue.Actions[0])
	}
	if *byValue.Actions[1].Target != weth || byValue.Caps[0].Amount != "12000000" || byValue.Caps[1].Amount != "12000000" {
		t.Fatalf("token caps = %+v %+v", byValue.Actions, byValue.Caps)
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
		t.Fatalf("{{value}} over a node output must stay unresolved, got %#v", loopValue)
	}
	if got := CoverageRefusal(covered, loopValue); got == nil {
		t.Fatal("an unresolved loop value must not be treated as covered")
	}
	if _, _, err := MergeSkillGrant(nil, PolicyAddition{}, []*avsproto.Task{nodeOutput}, skillSepolia, now, time.Hour); !unresolvedConflict(t, err, "weekly") {
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

// loopEthTask is an ETH pay compiled from a loop: destination and amount are
// templates, and the input is whatever the caller names.
func loopEthTask(t *testing.T, input, destination, amountExpr string, runs int64, settings map[string]any) *avsproto.Task {
	t.Helper()
	raw, err := structpb.NewValue(settings)
	if err != nil {
		t.Fatal(err)
	}
	return &avsproto.Task{
		Id:           "eth-pay",
		Name:         "Weekly ETH Pay",
		MaxExecution: runs,
		InputVariables: map[string]*structpb.Value{
			"settings": raw,
		},
		Nodes: []*avsproto.TaskNode{{
			TaskType: &avsproto.TaskNode_Loop{
				Loop: &avsproto.LoopNode{
					Config: &avsproto.LoopNode_Config{InputVariable: input},
					Runner: &avsproto.LoopNode_EthTransfer{
						EthTransfer: &avsproto.ETHTransferNode{
							Config: &avsproto.ETHTransferNode_Config{
								Destination: destination,
								Amount:      amountExpr,
								ChainId:     skillSepolia,
							},
						},
					},
				},
			},
		}},
	}
}

func TestEthPaySettingsListBindsValue(t *testing.T) {
	now := skillNow()
	a1 := "0x0000000000000000000000000000000000000001"
	a2 := "0x0000000000000000000000000000000000000002"
	task := loopEthTask(t, "{{settings.recipients}}", "{{value}}", "{{settings.token_amount.amount}}", 12, map[string]any{
		"recipients": []any{a1, a2},
		"token_amount": map[string]any{
			"amount": "1000000000000000",
		},
	})
	need := DeriveWorkflowNeeds(task, nil, scheduleFromTask(task, now), skillSepolia)[skillSepolia]
	if need == nil || need.Unresolved || need.CapNeedsInput || need.NativeSpendCap == nil {
		t.Fatalf("eth settings list = %#v", need)
	}
	if need.NativeSpendCap.Amount != "24000000000000000" {
		t.Fatalf("native cap = %s, want 1e15 x 12 x 2", need.NativeSpendCap.Amount)
	}
	if len(need.NativeRecipients) != 2 || need.NativeRecipients[0].Hex() != common.HexToAddress(a1).Hex() || need.NativeRecipients[1].Hex() != common.HexToAddress(a2).Hex() {
		t.Fatalf("recipients = %+v", need.NativeRecipients)
	}
	perms, _, err := MergeSkillGrant(nil, PolicyAddition{}, []*avsproto.Task{task}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if perms.NativeSpendCap == nil || perms.NativeSpendCap.Amount != "24000000000000000" || len(perms.NativeRecipients) != 2 {
		t.Fatalf("merged eth pay = %+v recipients %d", perms.NativeSpendCap, len(perms.NativeRecipients))
	}

	for _, input := range []string{"{{split1.data}}", "{{filter1.data}}"} {
		unread := loopEthTask(t, input, "{{value}}", "{{settings.token_amount.amount}}", 12, map[string]any{
			"recipients":   []any{a1, a2},
			"token_amount": map[string]any{"amount": "1000000000000000"},
		})
		got := DeriveWorkflowNeeds(unread, nil, scheduleFromTask(unread, now), skillSepolia)[skillSepolia]
		if got == nil || !got.Unresolved || len(got.NativeRecipients) != 0 {
			t.Fatalf("%s must stay unresolved with no recipients, got %#v", input, got)
		}
		if _, _, err := MergeSkillGrant(nil, PolicyAddition{}, []*avsproto.Task{unread}, skillSepolia, now, time.Hour); !unresolvedConflict(t, err, "eth-pay") {
			t.Fatalf("%s must fail the merge closed, got %v", input, err)
		}
	}

	rows := loopEthTask(t, "{{settings.rows}}", "{{value.recipient}}", "{{value.amount}}", 1, map[string]any{
		"rows": []any{
			map[string]any{"recipient": a1, "amount": "1000000000000000"},
			map[string]any{"recipient": a2, "amount": "2000000000000000"},
		},
	})
	rowNeed := DeriveWorkflowNeeds(rows, nil, scheduleFromTask(rows, now), skillSepolia)[skillSepolia]
	if rowNeed == nil || rowNeed.Unresolved || rowNeed.CapNeedsInput || rowNeed.NativeSpendCap == nil || rowNeed.NativeSpendCap.Amount != "3000000000000000" {
		t.Fatalf("{{value.x}} rows = %#v", rowNeed)
	}
	if len(rowNeed.NativeRecipients) != 2 {
		t.Fatalf("row recipients = %+v", rowNeed.NativeRecipients)
	}
	if _, _, err := MergeSkillGrant(nil, PolicyAddition{}, []*avsproto.Task{rows}, skillSepolia, now, time.Hour); err != nil {
		t.Fatal(err)
	}

	// A whole object is not an address. Binding {{value}} must not stringify it.
	object := loopEthTask(t, "{{settings.rows}}", "{{value}}", "{{value.amount}}", 1, map[string]any{
		"rows": []any{map[string]any{"recipient": a1, "amount": "1000000000000000"}},
	})
	if got := DeriveWorkflowNeeds(object, nil, scheduleFromTask(object, now), skillSepolia)[skillSepolia]; got == nil || !got.Unresolved || len(got.NativeRecipients) != 0 {
		t.Fatalf("object {{value}} must stay unresolved, got %#v", got)
	}

	bad := loopEthTask(t, "{{settings.recipients}}", "{{value}}", "{{settings.token_amount.amount}}", 12, map[string]any{
		"recipients":   []any{a1, "not-an-address"},
		"token_amount": map[string]any{"amount": "1000000000000000"},
	})
	if got := DeriveWorkflowNeeds(bad, nil, scheduleFromTask(bad, now), skillSepolia)[skillSepolia]; got == nil || !got.Unresolved {
		t.Fatalf("a non-address element must fail closed, got %#v", got)
	}
	if _, _, err := MergeSkillGrant(nil, PolicyAddition{}, []*avsproto.Task{bad}, skillSepolia, now, time.Hour); !unresolvedConflict(t, err, "eth-pay") {
		t.Fatalf("bad element merge = %v", err)
	}

	six := make([]any, MaxNativeRecipients+1)
	for i := range six {
		six[i] = common.BytesToAddress([]byte{byte(i + 1)}).Hex()
	}
	wide := loopEthTask(t, "{{settings.recipients}}", "{{value}}", "{{settings.token_amount.amount}}", 1, map[string]any{
		"recipients":   six,
		"token_amount": map[string]any{"amount": "1"},
	})
	wideNeed := DeriveWorkflowNeeds(wide, nil, scheduleFromTask(wide, now), skillSepolia)[skillSepolia]
	if wideNeed == nil || wideNeed.Unresolved || len(wideNeed.NativeRecipients) != MaxNativeRecipients+1 {
		t.Fatalf("six recipients must still be derived, got %#v", wideNeed)
	}
	widePerms, _, err := MergeSkillGrant(nil, PolicyAddition{}, []*avsproto.Task{wide}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := widePerms.Validate(); err == nil || !strings.Contains(err.Error(), "at most 5") {
		t.Fatalf("a list longer than %d native recipients must still fail validation, got %v", MaxNativeRecipients, err)
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
	if _, err := classifyRunnerCoverage(in, []*avsproto.Task{covered, other}, now, "01LIVE"); !errors.Is(err, ErrSessionPolicyNotCovering) {
		t.Fatalf("uncovered swap must 409, got %v", err)
	}
	var conflict *PolicyConflictError
	_, err := classifyRunnerCoverage(in, []*avsproto.Task{covered, other}, now, "01LIVE")
	if !errors.As(err, &conflict) || len(conflict.AffectedTaskIDs) != 1 || conflict.AffectedTaskIDs[0] != "swap" || conflict.PolicyID != "01LIVE" {
		t.Fatalf("affected = %#v", err)
	}
	in.DropTaskIDs = []string{"swap"}
	dropped, err := classifyRunnerCoverage(in, []*avsproto.Task{covered, other, {Id: "note", Name: "Ping"}}, now, "01LIVE")
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 1 || dropped[0] != "swap" {
		t.Fatalf("dropped = %#v", dropped)
	}
}

func TestClassifyRunnerCoverageSharedCap(t *testing.T) {
	now := skillNow()
	later := now.Add(30 * 24 * time.Hour).UnixMilli()
	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
	ten := transferCalldata(payee, big.NewInt(10))
	first := skillWriteTask("a", "Pay A", skillUSDC, ten, skillSepolia, 1, 0, later)
	second := skillWriteTask("b", "Pay B", skillUSDC, ten, skillSepolia, 1, 0, later)
	other := skillWriteTask("w", "Pay WETH", skillWETH, ten, skillSepolia, 1, 0, later)
	usdc := []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)}
	in := SessionPolicyInput{
		ChainID: skillSepolia,
		Permissions: SessionPermissions{
			AllowedActions: usdc,
			SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "10")},
			ValidUntilMs:   later,
		},
	}
	tasks := []*avsproto.Task{first, other, second}
	_, err := classifyRunnerCoverage(in, []*avsproto.Task{first, second}, now, "01LIVE")
	var conflict *PolicyConflictError
	if !errors.As(err, &conflict) || conflict.Code != SessionPolicyNotCoveringCode || conflict.PolicyID != "01LIVE" {
		t.Fatalf("shared cap = %#v", err)
	}
	if errors.Is(err, ErrSessionPolicyUnsized) || conflict.Code == SessionPolicyTargetUnresolvedCode {
		t.Fatalf("shared cap must stay not-covering, got %#v", err)
	}
	if conflict.Detail != combinedSpendShortDetail {
		t.Fatalf("detail = %q", conflict.Detail)
	}
	if len(conflict.AffectedTaskIDs) != 2 || conflict.AffectedTaskIDs[0] != "a" || conflict.AffectedTaskIDs[1] != "b" {
		t.Fatalf("affected = %#v", conflict.AffectedTaskIDs)
	}

	in.DropTaskIDs = []string{"b"}
	dropped, err := classifyRunnerCoverage(in, []*avsproto.Task{first, second}, now, "01LIVE")
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 1 || dropped[0] != "b" {
		t.Fatalf("dropped = %#v", dropped)
	}

	in.DropTaskIDs = nil
	in.Permissions.SpendCaps = []model.ERC20SpendCap{skillCap(skillUSDC, "20")}
	if _, err := classifyRunnerCoverage(in, []*avsproto.Task{first, second}, now, "01LIVE"); err != nil {
		t.Fatalf("a cap of the sum must cover both, got %v", err)
	}

	// Dropping a task the cap already covers is not echoed.
	in.DropTaskIDs = []string{"b"}
	dropped, err = classifyRunnerCoverage(in, []*avsproto.Task{first, second}, now, "01LIVE")
	if err != nil || len(dropped) != 0 {
		t.Fatalf("unnecessary drop = %#v err %v", dropped, err)
	}

	in.DropTaskIDs = nil
	in.Permissions.AllowedActions = []model.AllowedAction{
		skillAction(skillUSDC, selectorTransfer),
		skillAction(skillWETH, selectorTransfer),
	}
	in.Permissions.SpendCaps = []model.ERC20SpendCap{skillCap(skillUSDC, "10"), skillCap(skillWETH, "10")}
	if _, err := classifyRunnerCoverage(in, []*avsproto.Task{first, other}, now, "01LIVE"); err != nil {
		t.Fatalf("distinct tokens that each fit must pass, got %v", err)
	}

	in.Permissions.SpendCaps = []model.ERC20SpendCap{skillCap(skillUSDC, "10"), skillCap(skillWETH, "10")}
	_, err = classifyRunnerCoverage(in, tasks, now, "01LIVE")
	if !errors.As(err, &conflict) || conflict.Code != SessionPolicyNotCoveringCode {
		t.Fatalf("shared token beside a fitting token = %#v", err)
	}
	if len(conflict.AffectedTaskIDs) != 2 || conflict.AffectedTaskIDs[0] != "a" || conflict.AffectedTaskIDs[1] != "b" {
		t.Fatalf("only the short token's tasks, in order: %#v", conflict.AffectedTaskIDs)
	}

	rec := payee
	ethA := skillEthTask("e1", "Send A", payee.Hex(), "10", 1, later)
	ethB := skillEthTask("e2", "Send B", payee.Hex(), "10", 1, later)
	native := SessionPolicyInput{
		ChainID: skillSepolia,
		Permissions: SessionPermissions{
			NativeRecipients: []*common.Address{&rec},
			NativeSpendCap:   &model.NativeSpendCap{Amount: "10"},
			ValidUntilMs:     later,
		},
	}
	_, err = classifyRunnerCoverage(native, []*avsproto.Task{ethA, ethB}, now, "01LIVE")
	if !errors.As(err, &conflict) || conflict.Code != SessionPolicyNotCoveringCode || conflict.Detail != combinedSpendShortDetail {
		t.Fatalf("shared native cap = %#v", err)
	}
	if len(conflict.AffectedTaskIDs) != 2 || conflict.AffectedTaskIDs[0] != "e1" || conflict.AffectedTaskIDs[1] != "e2" {
		t.Fatalf("native affected = %#v", conflict.AffectedTaskIDs)
	}
	native.Permissions.NativeSpendCap = &model.NativeSpendCap{Amount: "20"}
	if _, err := classifyRunnerCoverage(native, []*avsproto.Task{ethA, ethB}, now, "01LIVE"); err != nil {
		t.Fatalf("native cap of the sum must cover both, got %v", err)
	}
}

func skillEthTask(id, name, dest, amount string, maxExec, expiredAt int64) *avsproto.Task {
	return &avsproto.Task{
		Id:           id,
		Name:         name,
		MaxExecution: maxExec,
		ExpiredAt:    expiredAt,
		Nodes: []*avsproto.TaskNode{{
			TaskType: &avsproto.TaskNode_EthTransfer{
				EthTransfer: &avsproto.ETHTransferNode{
					Config: &avsproto.ETHTransferNode_Config{
						Destination: dest,
						Amount:      amount,
						ChainId:     skillSepolia,
					},
				},
			},
		}},
	}
}

func TestAuthorizationRank(t *testing.T) {
	order := []string{
		AuthNotCovered,
		AuthNoGrant,
		AuthTargetUnresolved,
		AuthCapNeedsInput,
		AuthCapTooLow,
		AuthExpiresTooSoon,
		AuthCovered,
	}
	prev := 100
	for _, status := range order {
		got := authorizationRank(status)
		if got >= prev {
			t.Fatalf("%s rank %d is not below the status before it", status, got)
		}
		prev = got
	}
	if authorizationRank(AuthTargetUnresolved) >= authorizationRank(AuthNotCovered) ||
		authorizationRank(AuthTargetUnresolved) >= authorizationRank(AuthNoGrant) ||
		authorizationRank(AuthTargetUnresolved) <= authorizationRank(AuthCapNeedsInput) {
		t.Fatal("target_unresolved must sit below not_covered and no_grant and above cap_needs_input")
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
	if got := BuildAuthorization(early, need, nil, SkillSchedule{Now: time.UnixMilli(0)}); got.Status != AuthExpiresTooSoon || got.Detail != "the grant ends before this workflow's window" {
		t.Fatalf("expiry: %#v", got)
	}
	open := usableGrant("01G", 0, need.Actions, []model.ERC20SpendCap{skillCap(skillUSDC, "10")})
	if got := BuildAuthorization(open, need, nil, SkillSchedule{}); got.Status != AuthCovered {
		t.Fatalf("zero expiry must not be too soon: %#v", got)
	}
	closedNow := skillNow()
	noEnd := *need
	noEnd.ValidUntilMs = 0
	closed := usableGrant("01G", closedNow.Add(-time.Hour).UnixMilli(), need.Actions, []model.ERC20SpendCap{skillCap(skillUSDC, "10")})
	if got := BuildAuthorization(closed, &noEnd, nil, SkillSchedule{Now: closedNow}); got.Status != AuthExpiresTooSoon || got.Detail != "the grant has expired" || got.PolicyID != closed.ID {
		t.Fatalf("expired grant with no end must be expires_too_soon: %#v", got)
	}
	if got := BuildAuthorization(open, &noEnd, nil, SkillSchedule{Now: closedNow}); got.Status != AuthCovered {
		t.Fatalf("no recorded expiry must stay covered with no end: %#v", got)
	}
	shortExpired := usableGrant("01G", closedNow.Add(-time.Hour).UnixMilli(), need.Actions, []model.ERC20SpendCap{skillCap(skillUSDC, "1")})
	if got := BuildAuthorization(shortExpired, need, nil, SkillSchedule{Now: closedNow}); got.Status != AuthCapTooLow {
		t.Fatalf("a short cap still outranks an expired grant: %#v", got)
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
	if got := BuildAuthorization(open, unresolved, nil, SkillSchedule{}); got.Status != AuthTargetUnresolved || got.Detail != "a fund-moving target could not be resolved" {
		t.Fatalf("unresolved target with a grant: %#v", got)
	}
	// A covered observed call does not clear the static miss. One iteration
	// is not the whole loop.
	report := &SessionGrantReport{}
	report.observeCalls([]PlannedCall{{
		Target:   common.HexToAddress(skillUSDC),
		Selector: selectorTransfer,
		Calldata: common.FromHex(transferCalldata(common.HexToAddress("0x0000000000000000000000000000000000000001"), big.NewInt(1))),
	}})
	if got := BuildAuthorization(open, unresolved, report, SkillSchedule{}); got.Status != AuthTargetUnresolved || got.Required == nil || !got.Required.Unresolved {
		t.Fatalf("observed call must not clear an unresolved target: %#v", got)
	}
	// A call the report saw outside the grant is not_covered first. The
	// static miss stays on Required so one iteration cannot mark the loop read.
	outside := usableGrant("01OUT", 0, []model.AllowedAction{skillAction(skillWETH, selectorApprove)}, nil)
	missed := &SessionGrantReport{}
	missed.notePolicy(outside)
	usdcCall := PlannedCall{
		Target:   common.HexToAddress(skillUSDC),
		Selector: selectorTransfer,
		Calldata: common.FromHex(transferCalldata(common.HexToAddress("0x0000000000000000000000000000000000000001"), big.NewInt(1))),
	}
	missed.observeCalls([]PlannedCall{usdcCall})
	missed.noteGrantMiss("a planned call is outside the usable grant", []PlannedCall{usdcCall})
	if got := BuildAuthorization(outside, unresolved, missed, SkillSchedule{}); got.Status != AuthNotCovered || got.Required == nil || !got.Required.Unresolved {
		t.Fatalf("an uncovered observed call must stay not_covered and keep the unresolved target: %#v", got)
	}
}

func TestTasksExceptDropped(t *testing.T) {
	pay := skillWriteTask("pay", "Pay", skillUSDC, "0x", skillSepolia, 1, 0, 0)
	tmpl := skillWriteTask("tmpl", "Template", "{{settings.token}}", "0x", skillSepolia, 1, 0, 0)
	kept, dropped := tasksExceptDropped([]*avsproto.Task{nil, pay, tmpl, {Name: "blank"}}, []string{" TMPL "})
	if len(dropped) != 1 || dropped[0] != "tmpl" {
		t.Fatalf("dropped = %#v", dropped)
	}
	if len(kept) != 2 || kept[0].GetId() != "pay" || kept[1].GetId() != "" {
		t.Fatalf("kept ids = %q %q", kept[0].GetId(), kept[1].GetId())
	}
}

func TestMergeSkillGrantUnresolvedBeatsUnsized(t *testing.T) {
	now := skillNow()
	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
	// Unknown run count: the amount is not a fixed total, and the token is known.
	unsized := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(1)), skillSepolia, 0, 0, 0)
	// Known runs, unreadable contract. Typing a cap cannot name this target.
	split := skillWriteTask("split", "Split Incoming Payments", "{{value.tokenAddress}}", transferCalldata(payee, big.NewInt(1)), skillSepolia, 5, 0, 0)
	batch := skillWriteTask("batch", "On-Demand Batch", "{{filter1.data}}", transferCalldata(payee, big.NewInt(1)), skillSepolia, 1, 0, 0)

	_, _, err := MergeSkillGrant(nil, PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "12")},
	}, []*avsproto.Task{unsized, split}, skillSepolia, now, time.Hour)
	if !unresolvedConflict(t, err, "split") || errors.Is(err, ErrSessionPolicyUnsized) {
		t.Fatalf("an unread target must outrank an unsized amount, got %v", err)
	}
	var conflict *PolicyConflictError
	if !errors.As(err, &conflict) || len(conflict.AffectedTaskIDs) != 1 || conflict.PolicyID != "" {
		t.Fatalf("unsized task must not be listed as unread, got %#v", err)
	}

	_, _, err = MergeSkillGrant(nil, PolicyAddition{}, []*avsproto.Task{split, batch}, skillSepolia, now, time.Hour)
	if !errors.As(err, &conflict) || conflict.Code != SessionPolicyTargetUnresolvedCode {
		t.Fatalf("two unread targets = %v", err)
	}
	if len(conflict.AffectedTaskIDs) != 2 || conflict.AffectedTaskIDs[0] != "split" || conflict.AffectedTaskIDs[1] != "batch" {
		t.Fatalf("affected = %#v", conflict.AffectedTaskIDs)
	}
	if conflict.Detail != "enabled automations move funds to a target the grant cannot resolve" {
		t.Fatalf("detail = %q", conflict.Detail)
	}

	// A carried grant does not fail closed on the unread task. The current
	// rows stay; the unresolved task adds no target and no cap.
	current := usableGrant("01OLD", now.Add(24*time.Hour).UnixMilli(), []model.AllowedAction{
		skillAction(skillUSDC, selectorTransfer),
		skillAction(skillWETH, selectorApprove),
	}, []model.ERC20SpendCap{skillCap(skillUSDC, "24"), skillCap(skillWETH, "5")})
	carried, _, err := MergeSkillGrant(current, PolicyAddition{}, []*avsproto.Task{split}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatalf("a carried grant must keep an unresolved task from freezing the merge, got %v", err)
	}
	if got, ok := spendCapAmount(carried, common.HexToAddress(skillUSDC)); !ok || got != "24" {
		t.Fatalf("USDC cap = %q, want the stored 24", got)
	}
	if got, ok := spendCapAmount(carried, common.HexToAddress(skillWETH)); !ok || got != "5" {
		t.Fatalf("WETH cap = %q, want the stored 5", got)
	}
	if !actionHasSelector(carried.AllowedActions, skillUSDC, selectorTransfer) || !actionHasSelector(carried.AllowedActions, skillWETH, selectorApprove) {
		t.Fatalf("carried actions = %+v", carried.AllowedActions)
	}

	kept, dropped := tasksExceptDropped([]*avsproto.Task{unsized, split}, []string{"split"})
	if len(dropped) != 1 || dropped[0] != "split" || len(kept) != 1 {
		t.Fatalf("prepare filter = kept %d dropped %#v", len(kept), dropped)
	}
	// The kept transfer is unsized, so it adds no number. The carried stored
	// cap plus the addition is the total. That is not a 400.
	unsizedKept, _, err := MergeSkillGrant(current, PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "12")},
	}, kept, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatalf("unsized transfer on a carried token must not fail the merge, got %v", err)
	}
	if got, ok := spendCapAmount(unsizedKept, common.HexToAddress(skillUSDC)); !ok || got != "36" {
		t.Fatalf("USDC cap = %q, want max(stored 24, unsized 0) + 12", got)
	}
	if got, ok := spendCapAmount(unsizedKept, common.HexToAddress(skillWETH)); !ok || got != "5" {
		t.Fatalf("WETH cap = %q, want the stored 5", got)
	}

	sized := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(1)), skillSepolia, 1, 0, now.Add(24*time.Hour).UnixMilli())
	kept, _ = tasksExceptDropped([]*avsproto.Task{sized, split}, []string{"split"})
	perms, _, err := MergeSkillGrant(current, PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "12")},
	}, kept, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := spendCapAmount(perms, common.HexToAddress(skillUSDC)); !ok || got != "36" {
		t.Fatalf("USDC cap = %q, want max(stored 24, sized 1) + 12", got)
	}
	if !actionHasSelector(perms.AllowedActions, skillWETH, selectorApprove) {
		t.Fatal("the carried WETH approve was dropped")
	}
}

func TestClassifyRunnerCoverageUnresolved(t *testing.T) {
	now := skillNow()
	later := now.Add(30 * 24 * time.Hour).UnixMilli()
	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
	data := transferCalldata(payee, big.NewInt(1))
	covered := skillWriteTask("pay", "Pay", skillUSDC, data, skillSepolia, 1, 0, later)
	split := skillWriteTask("split", "Split Incoming Payments", "{{value.tokenAddress}}", data, skillSepolia, 5, 0, 0)
	batch := skillWriteTask("batch", "On-Demand Batch", "{{filter1.data}}", data, skillSepolia, 1, 0, 0)
	swap := skillWriteTask("swap", "Swap", skillWETH, data, skillSepolia, 1, 0, later)
	in := SessionPolicyInput{
		ChainID: skillSepolia,
		Permissions: SessionPermissions{
			AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
			SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "100")},
			ValidUntilMs:   later,
		},
	}
	_, err := classifyRunnerCoverage(in, []*avsproto.Task{covered, split}, now, "01LIVE")
	var conflict *PolicyConflictError
	if !errors.As(err, &conflict) || conflict.Code != SessionPolicyTargetUnresolvedCode || conflict.PolicyID != "01LIVE" || !errors.Is(err, ErrSessionPolicyNotCovering) || errors.Is(err, ErrSessionPolicyUnsized) {
		t.Fatalf("unread target = %#v", err)
	}
	if len(conflict.AffectedTaskIDs) != 1 || conflict.AffectedTaskIDs[0] != "split" {
		t.Fatalf("affected = %#v", conflict.AffectedTaskIDs)
	}
	if conflict.Detail != "Split Incoming Payments moves funds to a target the grant cannot resolve" {
		t.Fatalf("detail = %q", conflict.Detail)
	}

	in.DropTaskIDs = []string{"split"}
	dropped, err := classifyRunnerCoverage(in, []*avsproto.Task{covered, split}, now, "01LIVE")
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 1 || dropped[0] != "split" {
		t.Fatalf("dropped = %#v", dropped)
	}

	in.DropTaskIDs = nil
	_, err = classifyRunnerCoverage(in, []*avsproto.Task{split, batch}, now, "01LIVE")
	if !errors.As(err, &conflict) || conflict.Code != SessionPolicyTargetUnresolvedCode {
		t.Fatalf("two unread targets = %v", err)
	}
	if len(conflict.AffectedTaskIDs) != 2 || conflict.AffectedTaskIDs[0] != "split" || conflict.AffectedTaskIDs[1] != "batch" {
		t.Fatalf("affected = %#v", conflict.AffectedTaskIDs)
	}
	if conflict.Detail != "enabled automations move funds to a target the grant cannot resolve" {
		t.Fatalf("detail = %q", conflict.Detail)
	}

	_, err = classifyRunnerCoverage(in, []*avsproto.Task{swap, split}, now, "01LIVE")
	if !errors.As(err, &conflict) || conflict.Code != SessionPolicyTargetUnresolvedCode {
		t.Fatalf("mixed miss = %v", err)
	}
	if len(conflict.AffectedTaskIDs) != 2 || conflict.AffectedTaskIDs[0] != "swap" || conflict.AffectedTaskIDs[1] != "split" {
		t.Fatalf("mixed affected = %#v", conflict.AffectedTaskIDs)
	}
}

func unresolvedConflict(t *testing.T, err error, taskID string) bool {
	t.Helper()
	var conflict *PolicyConflictError
	if !errors.As(err, &conflict) || conflict.Code != SessionPolicyTargetUnresolvedCode || !errors.Is(err, ErrSessionPolicyNotCovering) {
		return false
	}
	if errors.Is(err, ErrSessionPolicyUnsized) {
		return false
	}
	for _, id := range conflict.AffectedTaskIDs {
		if id == taskID {
			return true
		}
	}
	return false
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

func skillManualTransfer(id string, expiredAt int64) *avsproto.Task {
	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
	task := skillWriteTask(id, "Pay", skillUSDC, transferCalldata(payee, big.NewInt(1)), skillSepolia, 0, 0, expiredAt)
	task.Trigger = &avsproto.TaskTrigger{
		Id:   "trg",
		Name: "manual",
		Type: avsproto.TriggerType_TRIGGER_TYPE_MANUAL,
		TriggerType: &avsproto.TaskTrigger_Manual{
			Manual: &avsproto.ManualTrigger{},
		},
	}
	return task
}

func storeCoveringGrant(t *testing.T, db storage.Storage, id string, owner, runner common.Address, until int64) {
	t.Helper()
	storeChainGrant(t, db, id, owner, runner, skillSepolia, until, []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)})
}

func storeChainGrant(t *testing.T, db storage.Storage, id string, owner, runner common.Address, chain, until int64, actions []model.AllowedAction) {
	t.Helper()
	usdc := common.HexToAddress(skillUSDC)
	policy := usableGrant(id, until, actions, []model.ERC20SpendCap{skillCap(skillUSDC, "10")})
	policy.Owner = &owner
	policy.Runner = &runner
	policy.ChainID = chain
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
}

func deployCheckEngine(db storage.Storage) *Engine {
	return &Engine{
		db:                db,
		config:            &config.Config{SessionPolicyDeployCheck: true},
		smartWalletConfig: &config.SmartWalletConfig{ChainID: skillSepolia},
	}
}

func skillWorkflow(task *avsproto.Task, runner common.Address) *model.Workflow {
	write := &model.Workflow{Task: task}
	write.SmartWalletAddress = runner.Hex()
	return write
}

func TestDeployCheckRefusesExpiredGrantWithNoEnd(t *testing.T) {
	db := testutil.TestMustDB()
	t.Cleanup(func() { storage.Destroy(db.(*storage.BadgerStorage)) })
	on := deployCheckEngine(db)
	now := time.Now()

	t.Run("manual trigger with no end", func(t *testing.T) {
		owner := common.HexToAddress("0x0000000000000000000000000000000000000011")
		runner := common.HexToAddress("0x0000000000000000000000000000000000000012")
		task := skillManualTransfer("pay", 0)
		need := DeriveWorkflowNeeds(task, nil, scheduleFromTask(task, now), skillSepolia)[skillSepolia]
		if need == nil || !need.HasFundMove || need.ValidUntilMs != 0 {
			t.Fatalf("manual transfer with no end = %#v", need)
		}
		expiredAt := now.Add(-time.Hour).UnixMilli()
		storeCoveringGrant(t, db, "01expiredmanual0000000000", owner, runner, expiredAt)
		_, err := on.enforceSessionPolicyDeployCheck(&model.User{Address: owner}, skillWorkflow(task, runner))
		var conflict *PolicyConflictError
		if !errors.As(err, &conflict) || conflict.Code != SessionPolicyExpiredCode || conflict.PolicyID != "01expiredmanual0000000000" {
			t.Fatalf("expired manual deploy = %#v", err)
		}
		wantDetail := fmt.Sprintf(
			"the runner's grant expired at %s; grant again before deploying this workflow",
			time.UnixMilli(expiredAt).UTC().Format(time.RFC3339),
		)
		if conflict.Detail != wantDetail || len(conflict.AffectedTaskIDs) != 1 || conflict.AffectedTaskIDs[0] != "pay" {
			t.Fatalf("conflict = %#v", conflict)
		}
		mu := sessionAuthorityLock(skillSepolia, owner, runner)
		if !mu.TryLock() {
			t.Fatal("a refusal must release the runner lock")
		}
		mu.Unlock()
	})

	t.Run("no recorded expiry still deploys", func(t *testing.T) {
		owner := common.HexToAddress("0x0000000000000000000000000000000000000013")
		runner := common.HexToAddress("0x0000000000000000000000000000000000000014")
		storeCoveringGrant(t, db, "01openmanual00000000000000", owner, runner, 0)
		unlock, err := on.enforceSessionPolicyDeployCheck(&model.User{Address: owner}, skillWorkflow(skillManualTransfer("pay", 0), runner))
		if err != nil {
			t.Fatal(err)
		}
		mu := sessionAuthorityLock(skillSepolia, owner, runner)
		if mu.TryLock() {
			mu.Unlock()
			unlock()
			t.Fatal("a passing deploy check must hold the runner lock until unlock")
		}
		unlock()
	})

	t.Run("notification still deploys on an expired grant", func(t *testing.T) {
		owner := common.HexToAddress("0x0000000000000000000000000000000000000015")
		runner := common.HexToAddress("0x0000000000000000000000000000000000000016")
		storeCoveringGrant(t, db, "01expirednote0000000000000", owner, runner, now.Add(-time.Hour).UnixMilli())
		note := skillWorkflow(&avsproto.Task{Id: "note", Name: "Ping"}, runner)
		if _, err := on.enforceSessionPolicyDeployCheck(&model.User{Address: owner}, note); err != nil {
			t.Fatal(err)
		}
		mu := sessionAuthorityLock(skillSepolia, owner, runner)
		if !mu.TryLock() {
			t.Fatal("a notification-only workflow must not take the runner lock")
		}
		mu.Unlock()
	})

	t.Run("future end on an already expired grant", func(t *testing.T) {
		owner := common.HexToAddress("0x0000000000000000000000000000000000000017")
		runner := common.HexToAddress("0x0000000000000000000000000000000000000018")
		storeCoveringGrant(t, db, "01expiredwindow00000000000", owner, runner, now.Add(-time.Hour).UnixMilli())
		task := skillManualTransfer("pay", now.Add(48*time.Hour).UnixMilli())
		_, err := on.enforceSessionPolicyDeployCheck(&model.User{Address: owner}, skillWorkflow(task, runner))
		var conflict *PolicyConflictError
		if !errors.As(err, &conflict) || conflict.Code != SessionPolicyExpiredCode {
			t.Fatalf("already-expired grant = %#v", err)
		}
	})

	t.Run("live grant that ends first stays not covering", func(t *testing.T) {
		owner := common.HexToAddress("0x0000000000000000000000000000000000000019")
		runner := common.HexToAddress("0x000000000000000000000000000000000000001a")
		storeCoveringGrant(t, db, "01earlywindow0000000000000", owner, runner, now.Add(time.Hour).UnixMilli())
		task := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(common.HexToAddress("0x0000000000000000000000000000000000000001"), big.NewInt(1)), skillSepolia, 1, 0, now.Add(48*time.Hour).UnixMilli())
		need := DeriveWorkflowNeeds(task, nil, scheduleFromTask(task, now), skillSepolia)[skillSepolia]
		grant := usableGrant("01earlywindow0000000000000", now.Add(time.Hour).UnixMilli(), []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)}, []model.ERC20SpendCap{skillCap(skillUSDC, "10")})
		if got := CoverageRefusal(grant, need); got == nil || got.Code != SessionPolicyNotCoveringCode || !strings.Contains(got.Detail, "expired") {
			t.Fatalf("a live grant that ends first is still a coverage gap, got %#v", got)
		}
		_, err := on.enforceSessionPolicyDeployCheck(&model.User{Address: owner}, skillWorkflow(task, runner))
		var conflict *PolicyConflictError
		if !errors.As(err, &conflict) || conflict.Code != SessionPolicyNotCoveringCode {
			t.Fatalf("live grant that ends first = %#v", err)
		}
	})

	t.Run("uncovered chain then an expired grant", func(t *testing.T) {
		// Chain ids are visited in order, so chain 1's coverage gap is
		// recorded before Sepolia's expired grant. required and missing
		// must follow the expired grant, which is the one policyId names.
		owner := common.HexToAddress("0x0000000000000000000000000000000000000021")
		runner := common.HexToAddress("0x0000000000000000000000000000000000000022")
		payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
		low := skillWriteTask("pay", "Pay", skillWETH, transferCalldata(payee, big.NewInt(1)), 1, 1, 0, 0)
		high := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(1)), skillSepolia, 1, 0, 0)
		low.Nodes = append(low.Nodes, high.Nodes...)
		storeChainGrant(t, db, "01uncoveredlowchain000000", owner, runner, 1, now.Add(24*time.Hour).UnixMilli(), []model.AllowedAction{skillAction(skillWETH, selectorApprove)})
		expiredAt := now.Add(-time.Hour).UnixMilli()
		storeChainGrant(t, db, "01expiredhighchain00000000", owner, runner, skillSepolia, expiredAt, []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)})

		_, err := on.enforceSessionPolicyDeployCheck(&model.User{Address: owner}, skillWorkflow(low, runner))
		var conflict *PolicyConflictError
		if !errors.As(err, &conflict) || conflict.Code != SessionPolicyExpiredCode || conflict.PolicyID != "01expiredhighchain00000000" {
			t.Fatalf("mixed-chain expired deploy = %#v", err)
		}
		if conflict.Required == nil || conflict.Required.ChainID != skillSepolia {
			t.Fatalf("required = %#v", conflict.Required)
		}
		if len(conflict.Required.Actions) != 1 || conflict.Required.Actions[0].Target == nil || !strings.EqualFold(conflict.Required.Actions[0].Target.Hex(), skillUSDC) {
			t.Fatalf("required actions = %+v", conflict.Required.Actions)
		}
		if len(conflict.Missing) != 0 {
			t.Fatalf("expired response kept another chain's gaps: %+v", conflict.Missing)
		}
		for _, mu := range orderedSessionLocks([]int64{1, skillSepolia}, owner, runner) {
			if !mu.TryLock() {
				t.Fatal("a refusal must release the runner lock")
			}
			mu.Unlock()
		}
	})
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
