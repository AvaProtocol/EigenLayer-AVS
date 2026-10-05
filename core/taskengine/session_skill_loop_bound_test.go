package taskengine

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

const skillUSDT = "0x7169D38820dfd117C3FA1f22a697dBA58d90BA06"

// splitTemplateSource is the custom code SplitNodeUtils.generateSource emits
// for the shipped template: 0.2 of a 6-decimal token, 30 percent, and the rest.
const splitTemplateSource = `const input = BigInt({{eventTrigger.data.value}});
return [
  { name: "", tokenAddress: "{{eventTrigger.data.contractAddress}}", recipient: "", amount: (200000n).toString() },
  { name: "", tokenAddress: "{{eventTrigger.data.contractAddress}}", recipient: "", amount: (input * 30n / 100n).toString() },
  { name: "", tokenAddress: "{{eventTrigger.data.contractAddress}}", recipient: "", amount: (input - (200000n + input * 30n / 100n)).toString() }
];`

func splitSource(token string, exprs ...string) string {
	var b strings.Builder
	b.WriteString("const input = BigInt({{eventTrigger.data.value}});\nreturn [\n")
	for i, expr := range exprs {
		if i > 0 {
			b.WriteString(",\n")
		}
		fmt.Fprintf(&b, "  { name: \"team\", tokenAddress: %q, recipient: \"0x0000000000000000000000000000000000000001\", amount: (%s).toString() }", token, expr)
	}
	b.WriteString("\n];")
	return b.String()
}

func splitLoopTask(source string, queries [][]string, runs int64) *avsproto.Task {
	var qs []*avsproto.EventTrigger_Query
	for _, addrs := range queries {
		qs = append(qs, &avsproto.EventTrigger_Query{Addresses: addrs})
	}
	return &avsproto.Task{
		Id:           "split",
		Name:         "Split Incoming Payments",
		MaxExecution: runs,
		Trigger: &avsproto.TaskTrigger{
			Name: "eventTrigger",
			TriggerType: &avsproto.TaskTrigger_Event{
				Event: &avsproto.EventTrigger{
					Config: &avsproto.EventTrigger_Config{Queries: qs},
				},
			},
		},
		Nodes: []*avsproto.TaskNode{
			{
				Name: "split1",
				TaskType: &avsproto.TaskNode_CustomCode{
					CustomCode: &avsproto.CustomCodeNode{
						Config: &avsproto.CustomCodeNode_Config{Source: source},
					},
				},
			},
			loopTransferNode("loop1", "{{split1.data}}", "value", "{{value.tokenAddress}}", "{{value.recipient}}", "{{value.amount}}"),
		},
	}
}

func loopTransferNode(name, input, iter, contract, recipient, amount string) *avsproto.TaskNode {
	return &avsproto.TaskNode{
		Name: name,
		TaskType: &avsproto.TaskNode_Loop{
			Loop: &avsproto.LoopNode{
				Config: &avsproto.LoopNode_Config{InputVariable: input, IterVal: iter},
				Runner: &avsproto.LoopNode_ContractWrite{
					ContractWrite: &avsproto.ContractWriteNode{
						Config: &avsproto.ContractWriteNode_Config{
							ChainId:         skillSepolia,
							ContractAddress: contract,
							MethodCalls: []*avsproto.ContractWriteNode_MethodCall{{
								MethodName:   "transfer",
								MethodParams: []string{recipient, amount},
							}},
						},
					},
				},
			},
		},
	}
}

func batchLoopTask(t *testing.T, source string, runs int64, transfers []any) *avsproto.Task {
	t.Helper()
	task := &avsproto.Task{
		Id:           "batch",
		Name:         "On-Demand Batch Transfer",
		MaxExecution: runs,
		Nodes: []*avsproto.TaskNode{
			{
				Name: "code1",
				TaskType: &avsproto.TaskNode_CustomCode{
					CustomCode: &avsproto.CustomCodeNode{
						Config: &avsproto.CustomCodeNode_Config{Source: source},
					},
				},
			},
			{
				Name: "filter1",
				TaskType: &avsproto.TaskNode_Filter{
					Filter: &avsproto.FilterNode{
						Config: &avsproto.FilterNode_Config{
							Expression:    "value.fundable === true",
							InputVariable: "{{code1.data.transfers}}",
						},
					},
				},
			},
			loopTransferNode("loopTransfer", "{{filter1.data}}", "value", "{{value.token_amount.address}}", "{{value.recipient}}", "{{value.token_amount.amount}}"),
		},
	}
	if transfers == nil {
		return task
	}
	raw, err := structpb.NewValue(map[string]any{"transfers": transfers})
	if err != nil {
		t.Fatal(err)
	}
	task.InputVariables = map[string]*structpb.Value{"settings": raw}
	return task
}

func transferRow(token, amount string) map[string]any {
	return transferRowAmount(token, amount)
}

func transferRowAmount(token string, amount any) map[string]any {
	return map[string]any{
		"recipient": "0x0000000000000000000000000000000000000001",
		"token_amount": map[string]any{
			"address":  token,
			"amount":   amount,
			"decimals": "6",
		},
	}
}

func needCap(need *WorkflowNeed, token string) (string, bool) {
	if need == nil {
		return "", false
	}
	want := common.HexToAddress(token)
	for _, cap := range need.Caps {
		if cap.Token != nil && *cap.Token == want {
			return cap.Amount, true
		}
	}
	return "", false
}

func deriveSepolia(task *avsproto.Task, now time.Time) *WorkflowNeed {
	return DeriveWorkflowNeeds(task, nil, scheduleFromTask(task, now), skillSepolia)[skillSepolia]
}

func TestFixedSplitSizesEachTriggerToken(t *testing.T) {
	now := skillNow()
	// One run spends only the token that arrived, so each watched token is
	// capped at the full per-run sum times the runs still left.
	task := splitLoopTask(splitSource("{{eventTrigger.data.contractAddress}}", "200000n", "300000n"), [][]string{{skillUSDC, skillUSDT}}, 5)
	need := deriveSepolia(task, now)
	if need == nil || need.Unresolved || need.CapNeedsInput || need.CapCeiling || len(need.Caps) != 2 {
		t.Fatalf("fixed split = %#v", need)
	}
	if !actionHasSelector(need.Actions, skillUSDC, selectorTransfer) || !actionHasSelector(need.Actions, skillUSDT, selectorTransfer) {
		t.Fatalf("actions = %+v", need.Actions)
	}
	for _, token := range []string{skillUSDC, skillUSDT} {
		if got, ok := needCap(need, token); !ok || got != "2500000" {
			t.Fatalf("%s cap = %q, want 500000 x 5", token, got)
		}
	}
	perms, _, err := MergeSkillGrant(nil, PolicyAddition{}, []*avsproto.Task{task}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{skillUSDC, skillUSDT} {
		if got, ok := spendCapAmount(perms, common.HexToAddress(token)); !ok || got != "2500000" {
			t.Fatalf("merged %s = %q", token, got)
		}
	}

	// A literal token does not pull the other contracts the trigger watches.
	literal := splitLoopTask(splitSource(skillUSDC, "200000n"), [][]string{{skillUSDC, skillUSDT}}, 5)
	literal.Nodes[1].GetLoop().Config.IterVal = ""
	one := deriveSepolia(literal, now)
	if one == nil || one.Unresolved || one.CapNeedsInput || len(one.Actions) != 1 || len(one.Caps) != 1 {
		t.Fatalf("literal token = %#v", one)
	}
	if got, ok := needCap(one, skillUSDC); !ok || got != "1000000" {
		t.Fatalf("USDC cap = %q, want 200000 x 5", got)
	}
	if actionHasSelector(one.Actions, skillUSDT, selectorTransfer) {
		t.Fatal("a literal USDC split must not allow USDT")
	}

	// Unknown runs name the tokens and do not invent one run or a ceiling.
	open := splitLoopTask(splitSource(skillUSDC, "200000n"), [][]string{{skillUSDC}}, 0)
	unknown := deriveSepolia(open, now)
	if unknown == nil || unknown.Unresolved || !unknown.CapNeedsInput || unknown.CapCeiling || len(unknown.Caps) != 0 {
		t.Fatalf("unknown runs = %#v", unknown)
	}
	if !actionHasSelector(unknown.Actions, skillUSDC, selectorTransfer) {
		t.Fatalf("unknown-run actions = %+v", unknown.Actions)
	}
	capped, _, err := MergeSkillGrant(nil, PolicyAddition{
		AllowedActions: modelAllowed(skillUSDC),
		SpendCaps:      modelCap(skillUSDC, "40"),
	}, []*avsproto.Task{open}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := spendCapAmount(capped, common.HexToAddress(skillUSDC)); !ok || got != "40" {
		t.Fatalf("unknown-run merge cap = %q, want the addition only", got)
	}
	cover := usableGrant("01COVER", now.Add(time.Hour).UnixMilli(), modelAllowed(skillUSDC), modelCap(skillUSDC, "40"))
	if auth := BuildAuthorization(cover, unknown, nil, scheduleFromTask(open, now)); auth.Status != AuthCapNeedsInput {
		t.Fatalf("unknown runs stay cap_needs_input once a cap exists, got %#v", auth)
	}
}

func TestPercentageSplitAsksForACeiling(t *testing.T) {
	now := skillNow()
	task := splitLoopTask(splitTemplateSource, [][]string{{skillUSDC}, {skillUSDT}}, 5)
	need := deriveSepolia(task, now)
	if need == nil || need.Unresolved || !need.CapNeedsInput || !need.CapCeiling || len(need.Caps) != 0 || len(need.Actions) != 2 {
		t.Fatalf("percentage split = %#v", need)
	}
	if !actionHasSelector(need.Actions, skillUSDC, selectorTransfer) || !actionHasSelector(need.Actions, skillUSDT, selectorTransfer) {
		t.Fatalf("actions = %+v", need.Actions)
	}

	_, _, err := MergeSkillGrant(nil, PolicyAddition{}, []*avsproto.Task{task}, skillSepolia, now, time.Hour)
	if !errors.Is(err, ErrSessionPolicyUnsized) || unresolvedConflict(t, err, "split") {
		t.Fatalf("an empty addition must be unsized, not unresolved, got %v", err)
	}
	perms, _, err := MergeSkillGrant(nil, PolicyAddition{
		AllowedActions: modelAllowed(skillUSDC, skillUSDT),
		SpendCaps:      modelCap(skillUSDC, "10", skillUSDT, "20"),
	}, []*avsproto.Task{task}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := spendCapAmount(perms, common.HexToAddress(skillUSDC)); !ok || got != "10" {
		t.Fatalf("USDC cap = %q, want the addition only", got)
	}
	if got, ok := spendCapAmount(perms, common.HexToAddress(skillUSDT)); !ok || got != "20" {
		t.Fatalf("USDT cap = %q, want the addition only", got)
	}

	later := now.Add(24 * time.Hour).UnixMilli()
	missing := usableGrant("01MISS", later, modelAllowed(skillWETH), modelCap(skillWETH, "1"))
	if auth := BuildAuthorization(missing, need, nil, scheduleFromTask(task, now)); auth.Status != AuthCapNeedsInput || strings.Contains(auth.Detail, "could not be resolved") {
		t.Fatalf("missing ceiling = %#v", auth)
	}
	if auth := BuildAuthorization(nil, need, nil, scheduleFromTask(task, now)); auth.Status != AuthNoGrant || auth.Required == nil || len(auth.Required.Caps) != 0 || len(auth.Required.Actions) != 2 {
		t.Fatalf("no grant = %#v", auth)
	}
	cover := usableGrant("01COVER", later, modelAllowed(skillUSDC, skillUSDT), modelCap(skillUSDC, "10", skillUSDT, "20"))
	if auth := BuildAuthorization(cover, need, nil, scheduleFromTask(task, now)); auth.Status != AuthCovered || auth.Detail != ceilingSharedCapDetail {
		t.Fatalf("a positive ceiling must cover and say the cap is shared, got %#v", auth)
	}

	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
	call := PlannedCall{
		Target:   common.HexToAddress(skillUSDC),
		Selector: selectorTransfer,
		Calldata: common.FromHex(transferCalldata(payee, bigInt(1000))),
	}
	outside := &SessionGrantReport{}
	outside.notePolicy(missing)
	outside.observeCalls([]PlannedCall{call})
	outside.noteGrantMiss("outside", []PlannedCall{call})
	if auth := BuildAuthorization(missing, need, outside, scheduleFromTask(task, now)); auth.Status != AuthNotCovered || auth.Required == nil || !auth.Required.CapCeiling || len(auth.Required.Caps) != 0 {
		t.Fatalf("an observed miss must stay not_covered without a derived cap, got %#v", auth)
	}
	inside := &SessionGrantReport{}
	inside.notePolicy(cover)
	inside.observeCalls([]PlannedCall{call})
	if auth := BuildAuthorization(cover, need, inside, scheduleFromTask(task, now)); auth.Status != AuthCovered || auth.Required == nil || !auth.Required.CapCeiling || len(auth.Required.Caps) != 0 {
		t.Fatalf("one observed deposit must not become the cap, got %#v", auth)
	}
}

func TestSplitShapeStaysUnresolved(t *testing.T) {
	now := skillNow()
	queries := [][]string{{skillUSDC, skillUSDT}}
	cases := []struct {
		name string
		task *avsproto.Task
	}{
		{"extra statement", splitLoopTask(splitTemplateSource+"\nconst extra = 1;", queries, 5)},
		{"empty query", splitLoopTask(splitSource(skillUSDC, "200000n"), [][]string{{}}, 5)},
		{"no queries", splitLoopTask(splitSource(skillUSDC, "200000n"), nil, 5)},
		{"sentinel token", splitLoopTask(splitSource("0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "200000n"), queries, 5)},
		{"non-hex token", splitLoopTask(splitSource("not-a-token", "200000n"), queries, 5)},
		{"two token literals", splitLoopTask(splitSource(skillUSDC, "200000n")+"\n"+splitSource(skillUSDT, "1n"), queries, 5)},
	}
	// The two-literal case above is two programs concatenated, which is
	// already not the grammar. Also reject one program whose rows name
	// different tokens.
	mixed := splitLoopTask(splitSource(skillUSDC, "200000n"), queries, 5)
	mixed.Nodes[0].GetCustomCode().Config.Source = splitSource(skillUSDC, "200000n", "1n")
	mixed.Nodes[0].GetCustomCode().Config.Source = strings.Replace(mixed.Nodes[0].GetCustomCode().Config.Source, skillUSDC, skillUSDT, 1)
	cases = append(cases, struct {
		name string
		task *avsproto.Task
	}{"mixed row tokens", mixed})

	payable := splitLoopTask(splitSource(skillUSDC, "200000n"), queries, 5)
	value := "1"
	payable.Nodes[1].GetLoop().GetContractWrite().Config.Value = &value
	cases = append(cases, struct {
		name string
		task *avsproto.Task
	}{"payable runner", payable})

	for _, tc := range cases {
		got := deriveSepolia(tc.task, now)
		if got == nil || !got.Unresolved || len(got.Caps) != 0 {
			t.Fatalf("%s must stay unresolved with no cap, got %#v", tc.name, got)
		}
		if _, _, err := MergeSkillGrant(nil, PolicyAddition{
			AllowedActions: modelAllowed(skillUSDC),
			SpendCaps:      modelCap(skillUSDC, "100"),
		}, []*avsproto.Task{tc.task}, skillSepolia, now, time.Hour); !unresolvedConflict(t, err, "split") {
			t.Fatalf("%s merge = %v", tc.name, err)
		}
	}

	// No split1 node: the weekly-pay shape must not be treated as this template.
	bare := splitLoopTask(splitSource(skillUSDC, "200000n"), queries, 5)
	bare.Nodes = bare.Nodes[1:]
	if got := deriveSepolia(bare, now); got == nil || !got.Unresolved || len(got.Actions) != 0 {
		t.Fatalf("loop without split1 = %#v", got)
	}
}

func TestBatchNumericSizesFromSettings(t *testing.T) {
	now := skillNow()
	rows := []any{transferRow(skillUSDC, "100000"), transferRow(skillUSDC, "250000")}
	task := batchLoopTask(t, batchFundingSource, 3, rows)
	need := deriveSepolia(task, now)
	if need == nil || need.Unresolved || need.CapNeedsInput || need.CapCeiling || len(need.Actions) != 1 || len(need.Caps) != 1 {
		t.Fatalf("batch = %#v", need)
	}
	if got, ok := needCap(need, skillUSDC); !ok || got != "1050000" {
		t.Fatalf("USDC cap = %q, want (100000+250000) x 3", got)
	}
	crlf := batchLoopTask(t, strings.ReplaceAll(batchFundingSource, "\n", "\r\n"), 3, rows)
	if got := deriveSepolia(crlf, now); got == nil || got.Unresolved || needCapAmount(got, skillUSDC) != "1050000" {
		t.Fatalf("CRLF code1 = %#v", got)
	}

	both := batchLoopTask(t, batchFundingSource, 2, []any{transferRow(skillUSDC, "100000"), transferRow(skillUSDT, "200000")})
	two := deriveSepolia(both, now)
	if got, ok := needCap(two, skillUSDC); !ok || got != "200000" {
		t.Fatalf("USDC = %q", got)
	}
	if got, ok := needCap(two, skillUSDT); !ok || got != "400000" {
		t.Fatalf("USDT = %q", got)
	}

	open := batchLoopTask(t, batchFundingSource, 0, rows)
	unknown := deriveSepolia(open, now)
	if unknown == nil || unknown.Unresolved || !unknown.CapNeedsInput || unknown.CapCeiling || len(unknown.Caps) != 0 {
		t.Fatalf("maxExecution 0 = %#v", unknown)
	}
	if !actionHasSelector(unknown.Actions, skillUSDC, selectorTransfer) {
		t.Fatalf("actions = %+v", unknown.Actions)
	}
	capped, _, err := MergeSkillGrant(nil, PolicyAddition{
		AllowedActions: modelAllowed(skillUSDC),
		SpendCaps:      modelCap(skillUSDC, "75"),
	}, []*avsproto.Task{open}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := spendCapAmount(capped, common.HexToAddress(skillUSDC)); !ok || got != "75" {
		t.Fatalf("unknown batch cap = %q", got)
	}
	cover := usableGrant("01COVER", now.Add(time.Hour).UnixMilli(), modelAllowed(skillUSDC), modelCap(skillUSDC, "75"))
	if auth := BuildAuthorization(cover, unknown, nil, scheduleFromTask(open, now)); auth.Status != AuthCapNeedsInput {
		t.Fatalf("unknown batch simulate = %#v", auth)
	}

	empty := batchLoopTask(t, batchFundingSource, 3, []any{})
	if got := deriveSepolia(empty, now); got != nil {
		t.Fatalf("empty transfers must not move funds, got %#v", got)
	}

	// A JSON number is how structpb stores a numeric amount. An exact integer
	// still sizes; it is not a ceiling.
	numbered := batchLoopTask(t, batchFundingSource, 2, []any{transferRowAmount(skillUSDC, float64(100000))})
	if got, ok := needCap(deriveSepolia(numbered, now), skillUSDC); !ok || got != "200000" {
		t.Fatalf("JSON number cap = %q", got)
	}
}

func TestBatchShapeStaysUnresolved(t *testing.T) {
	now := skillNow()
	rows := []any{transferRow(skillUSDC, "999999")}
	edited := batchLoopTask(t, strings.Replace(batchFundingSource, "fundable", "fundablf", 1), 3, rows)
	assertUnresolvedBatch(t, edited, now)

	badFilter := batchLoopTask(t, batchFundingSource, 3, rows)
	badFilter.Nodes[1].GetFilter().Config.Expression = "value.fundable == true"
	assertUnresolvedBatch(t, badFilter, now)

	negative := batchLoopTask(t, batchFundingSource, 3, []any{transferRow(skillUSDC, "100000"), transferRow(skillUSDC, "-1")})
	assertUnresolvedBatch(t, negative, now)

	// MAX is code1's "spend the rest" sentinel, and transfer cannot encode
	// it as a uint256. These values stay unrecognized, sibling rows included.
	for _, amount := range []any{"MAX", "max", "hello", "", " 100", "1.5", float64(1.5)} {
		bad := batchLoopTask(t, batchFundingSource, 3, []any{transferRow(skillUSDC, "100000"), transferRowAmount(skillUSDT, amount)})
		assertUnresolvedBatch(t, bad, now)
	}

	sentinel := batchLoopTask(t, batchFundingSource, 3, []any{transferRow("0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "100000")})
	assertUnresolvedBatch(t, sentinel, now)

	malformed := batchLoopTask(t, batchFundingSource, 3, []any{map[string]any{"recipient": "0x0000000000000000000000000000000000000001"}})
	assertUnresolvedBatch(t, malformed, now)

	missing := batchLoopTask(t, batchFundingSource, 3, nil)
	assertUnresolvedBatch(t, missing, now)
}

func TestCeilingCoverageIgnoresSiblingOrder(t *testing.T) {
	now := skillNow()
	payee := common.HexToAddress("0x0000000000000000000000000000000000000002")
	later := now.Add(60 * 24 * time.Hour).UnixMilli()
	sized := skillWriteTask("pay", "Weekly pay", skillUSDC, transferCalldata(payee, big.NewInt(1_000_000)), skillSepolia, 4, 0, later)
	split := splitLoopTask(splitTemplateSource, [][]string{{skillUSDC}}, 5)
	split.Id = "split"

	_, _, sizedFirst := MergeSkillGrant(nil, PolicyAddition{}, []*avsproto.Task{sized, split}, skillSepolia, now, time.Hour)
	_, _, splitFirst := MergeSkillGrant(nil, PolicyAddition{}, []*avsproto.Task{split, sized}, skillSepolia, now, time.Hour)
	if !errors.Is(sizedFirst, ErrSessionPolicyUnsized) || !errors.Is(splitFirst, ErrSessionPolicyUnsized) {
		t.Fatalf("empty addition: sized-first %v, split-first %v", sizedFirst, splitFirst)
	}

	addition := PolicyAddition{
		AllowedActions: modelAllowed(skillUSDC),
		SpendCaps:      modelCap(skillUSDC, "10"),
	}
	payThenSplit, _, err := MergeSkillGrant(nil, addition, []*avsproto.Task{sized, split}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	splitThenPay, _, err := MergeSkillGrant(nil, addition, []*avsproto.Task{split, sized}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, perms := range []SessionPermissions{payThenSplit, splitThenPay} {
		if got, ok := spendCapAmount(perms, common.HexToAddress(skillUSDC)); !ok || got != "4000010" {
			t.Fatalf("cap = %q, want the pay's 4000000 plus the addition 10", got)
		}
	}
}

func TestCarriedGrantDoesNotFreezeOnARecognizedSplit(t *testing.T) {
	now := skillNow()
	later := now.Add(24 * time.Hour).UnixMilli()
	task := splitLoopTask(splitTemplateSource, [][]string{{skillUSDC}}, 5)
	current := usableGrant("01OLD", later, modelAllowed(skillWETH), modelCap(skillWETH, "5"))
	perms, _, err := MergeSkillGrant(current, PolicyAddition{}, []*avsproto.Task{task}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if actionHasSelector(perms.AllowedActions, skillUSDC, selectorTransfer) {
		t.Fatalf("a ceiling the grant never capped must be omitted, actions %+v", perms.AllowedActions)
	}
	if got, ok := spendCapAmount(perms, common.HexToAddress(skillWETH)); !ok || got != "5" {
		t.Fatalf("WETH cap = %q", got)
	}
	_, err = classifyCarriedCoverage(current, GrantRemainder{ERC20: map[common.Address]*big.Int{
		common.HexToAddress(skillWETH): bigInt(5),
	}}, SessionPolicyInput{ChainID: skillSepolia, Permissions: perms}, []*avsproto.Task{task}, now)
	if err != nil {
		t.Fatalf("a grant that already skipped the split must not 409, got %v", err)
	}

	holding := usableGrant("01HOLD", later, modelAllowed(skillUSDC), modelCap(skillUSDC, "24"))
	kept, _, err := MergeSkillGrant(holding, PolicyAddition{}, []*avsproto.Task{task}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := spendCapAmount(kept, common.HexToAddress(skillUSDC)); !ok || got != "24" {
		t.Fatalf("ceiling must not raise the carried 24, got %q", got)
	}
	added, _, err := MergeSkillGrant(holding, PolicyAddition{
		AllowedActions: modelAllowed(skillUSDC),
		SpendCaps:      modelCap(skillUSDC, "10"),
	}, []*avsproto.Task{task}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := spendCapAmount(added, common.HexToAddress(skillUSDC)); !ok || got != "34" {
		t.Fatalf("carried ceiling = %q, want max(24, 0) + 10", got)
	}
}

func assertUnresolvedBatch(t *testing.T, task *avsproto.Task, now time.Time) {
	t.Helper()
	got := deriveSepolia(task, now)
	if got == nil || !got.Unresolved || len(got.Caps) != 0 {
		t.Fatalf("batch shape = %#v", got)
	}
	if _, _, err := MergeSkillGrant(nil, PolicyAddition{
		AllowedActions: modelAllowed(skillUSDC),
		SpendCaps:      modelCap(skillUSDC, "100"),
	}, []*avsproto.Task{task}, skillSepolia, now, time.Hour); !unresolvedConflict(t, err, "batch") {
		t.Fatalf("merge = %v", err)
	}
}

func needCapAmount(need *WorkflowNeed, token string) string {
	got, _ := needCap(need, token)
	return got
}

func modelAllowed(tokens ...string) []model.AllowedAction {
	out := make([]model.AllowedAction, len(tokens))
	for i, token := range tokens {
		out[i] = skillAction(token, selectorTransfer)
	}
	return out
}

func modelCap(pairs ...string) []model.ERC20SpendCap {
	var out []model.ERC20SpendCap
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, skillCap(pairs[i], pairs[i+1]))
	}
	return out
}

func bigInt(n int64) *big.Int {
	return big.NewInt(n)
}
