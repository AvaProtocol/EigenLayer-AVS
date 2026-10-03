package taskengine

import (
	"bytes"
	"container/heap"
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/stretchr/testify/require"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func spendCapAmount(perms SessionPermissions, token common.Address) (string, bool) {
	for _, cap := range permissionCaps(perms) {
		if cap.Token != nil && *cap.Token == token {
			return cap.Amount, true
		}
	}
	return "", false
}

func TestCarryIgnoresRemainderWhenThereIsNoGrant(t *testing.T) {
	now := skillNow()
	addition := PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "12")},
	}
	task := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(common.HexToAddress("0x0000000000000000000000000000000000000001"), big.NewInt(1)), skillSepolia, 1, 0, 0)
	fresh, _, err := MergeSkillGrant(nil, addition, []*avsproto.Task{task}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	usdc := common.HexToAddress(skillUSDC)
	injected := &GrantRemainder{ERC20: map[common.Address]*big.Int{usdc: big.NewInt(1)}}
	withRem, _, err := MergeSkillGrantWithRemainder(nil, injected, addition, []*avsproto.Task{task}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !sameSessionPermissions(fresh, withRem) {
		t.Fatalf("a nil current grant must ignore the remainder\n fresh %+v\n rem %+v", fresh, withRem)
	}
}

func TestCarryDropsAZeroRemainderAndGivesTheScheduleBack(t *testing.T) {
	now := skillNow()
	usdc := common.HexToAddress(skillUSDC)
	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
	current := usableGrant("01OLD", now.Add(30*24*time.Hour).UnixMilli(),
		[]model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		[]model.ERC20SpendCap{skillCap(skillUSDC, "100")})

	// Chat-only: stored 100, chain has 60, nothing enabled still claims it.
	chat, changes, err := MergeSkillGrantWithRemainder(current, &GrantRemainder{ERC20: map[common.Address]*big.Int{usdc: big.NewInt(60)}}, PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "10")},
		ValidUntilMs:   now.Add(40 * 24 * time.Hour).UnixMilli(),
	}, nil, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := spendCapAmount(chat, usdc); !ok || got != "70" {
		t.Fatalf("chat cap = %q, want 60 + 10", got)
	}
	foundWas := false
	for _, line := range changes.Summary {
		if strings.Contains(line, "was 100") {
			t.Fatalf("previous amount used the stored total: %q", line)
		}
		if strings.Contains(line, "was 60") {
			foundWas = true
		}
	}
	if !foundWas {
		t.Fatalf("summary = %#v", changes.Summary)
	}

	// The schedule still needs 12 and the chain has 8. The need wins.
	sched := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(1)), skillSepolia, 12, 0, 0)
	bumped, _, err := MergeSkillGrantWithRemainder(current, &GrantRemainder{ERC20: map[common.Address]*big.Int{usdc: big.NewInt(8)}}, PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "5")},
		ValidUntilMs:   now.Add(40 * 24 * time.Hour).UnixMilli(),
	}, []*avsproto.Task{sched}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := spendCapAmount(bumped, usdc); !ok || got != "17" {
		t.Fatalf("schedule cap = %q, want max(8, 12) + 5", got)
	}

	// Nothing left, and nothing new asks for the token: omit it.
	spent := usableGrant(current.ID, current.ValidUntil, current.AllowedActions, current.ERC20SpendCaps)
	dropped, _, err := MergeSkillGrantWithRemainder(spent, &GrantRemainder{ERC20: map[common.Address]*big.Int{usdc: big.NewInt(0)}}, PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillWETH, selectorTransfer)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillWETH, "1")},
		ValidUntilMs:   now.Add(40 * 24 * time.Hour).UnixMilli(),
	}, nil, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := spendCapAmount(dropped, usdc); ok {
		t.Fatal("a zero remainder was installed")
	}
	if actionHasSelector(dropped.AllowedActions, skillUSDC, selectorTransfer) {
		t.Fatal("a spent transfer stayed on the grant")
	}
	for _, cap := range dropped.SpendCaps {
		if cap.Amount == "0" {
			t.Fatal("installed a zero cap")
		}
	}
}

func TestCarryKeepsARouterWhenTheTokenIsSpent(t *testing.T) {
	now := skillNow()
	usdc := common.HexToAddress(skillUSDC)
	dest := common.HexToAddress("0x00000000000000000000000000000000000000b1")
	current := usableGrant("01SWAP", now.Add(30*24*time.Hour).UnixMilli(),
		[]model.AllowedAction{
			skillAction(skillUSDC, selectorApprove),
			skillAction(skillRouter, selectorExactInputSingle),
		},
		[]model.ERC20SpendCap{skillCap(skillUSDC, "100")})
	perms, changes, err := MergeSkillGrantWithRemainder(current, &GrantRemainder{
		ERC20: map[common.Address]*big.Int{usdc: big.NewInt(0)},
	}, PolicyAddition{
		NativeSpendCap:   &model.NativeSpendCap{Amount: "5"},
		NativeRecipients: []*common.Address{&dest},
		ValidUntilMs:     now.Add(40 * 24 * time.Hour).UnixMilli(),
	}, nil, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := spendCapAmount(perms, usdc); ok {
		t.Fatal("a spent USDC cap was installed")
	}
	if actionHasSelector(perms.AllowedActions, skillUSDC, selectorApprove) {
		t.Fatal("a spent approve stayed on the grant")
	}
	if !actionHasSelector(perms.AllowedActions, skillRouter, selectorExactInputSingle) {
		t.Fatal("the router row was dropped with the spent token")
	}
	if perms.NativeSpendCap == nil || perms.NativeSpendCap.Amount != "5" {
		t.Fatalf("native cap = %+v", perms.NativeSpendCap)
	}
	keptRouter := false
	for _, action := range changes.KeptActions {
		if action.Target != nil && *action.Target == common.HexToAddress(skillRouter) {
			keptRouter = true
		}
	}
	if !keptRouter {
		t.Fatalf("kept actions = %+v", changes.KeptActions)
	}
	perms.CodeAt = func(common.Address) ([]byte, error) { return nil, nil }
	if err := perms.Validate(); err != nil {
		t.Fatalf("a router with no ERC-20 cap must still be signable: %v", err)
	}
	if _, err := perms.HooksFor(1); err != nil {
		t.Fatalf("install calldata: %v", err)
	}

	bare := SessionPermissions{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		ValidUntilMs:   time.Now().Add(time.Hour).UnixMilli(),
	}
	if err := bare.Validate(); err == nil || !strings.Contains(err.Error(), "a grant needs an ERC-20 spend cap") {
		t.Fatalf("a transfer with no cap = %v", err)
	}
}

func TestCarryNeverCappedTokenIsNotSpent(t *testing.T) {
	now := skillNow()
	usdc := common.HexToAddress(skillUSDC)
	weth := common.HexToAddress(skillWETH)
	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
	current := usableGrant("01OLD", now.Add(30*24*time.Hour).UnixMilli(),
		[]model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		[]model.ERC20SpendCap{skillCap(skillUSDC, "100")})
	// The grant never capped WETH. A sized WETH transfer is a new cap, not
	// a spent one, so the summary has no "was 0".
	task := skillWriteTask("pay", "Pay", skillWETH, transferCalldata(payee, big.NewInt(5)), skillSepolia, 1, 0, 0)
	perms, changes, err := MergeSkillGrantWithRemainder(current, &GrantRemainder{ERC20: map[common.Address]*big.Int{usdc: big.NewInt(60)}}, PolicyAddition{
		ValidUntilMs: now.Add(40 * 24 * time.Hour).UnixMilli(),
	}, []*avsproto.Task{task}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := spendCapAmount(perms, weth); !ok || got != "5" {
		t.Fatalf("WETH cap = %q, want the sized need 5", got)
	}
	for _, change := range changes.CapChanges {
		if change.Token == weth && change.PreviousAmount != "" {
			t.Fatalf("never-capped WETH was treated as spent: %+v", change)
		}
	}
}

func TestCarryAppliedGrantRefusesAMissingRead(t *testing.T) {
	now := skillNow()
	current := usableGrant("01APPLIED", now.Add(24*time.Hour).UnixMilli(),
		[]model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		[]model.ERC20SpendCap{skillCap(skillUSDC, "100")})
	current.Grant.AppliedAt = 1
	_, _, err := MergeSkillGrant(current, PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "10")},
	}, nil, skillSepolia, now, time.Hour)
	if err == nil || !strings.Contains(err.Error(), "has no remaining-limit read") {
		t.Fatalf("applied grant with no read = %v", err)
	}
	if errors.Is(err, ErrSessionPolicyUnsized) {
		t.Fatal("a missed read must not look like an unsized cap")
	}
}

func TestCarryNativeUsesTheSameMaxAndRefusesAnUnsizedInstall(t *testing.T) {
	now := skillNow()
	dest := common.HexToAddress("0x0000000000000000000000000000000000000001")
	current := usableGrant("01ETH", now.Add(30*24*time.Hour).UnixMilli(), nil, nil)
	current.NativeSpendCap = &model.NativeSpendCap{Amount: "100"}
	current.NativeRecipients = []*common.Address{&dest}
	sized := skillEthTask("eth", "Send ETH", dest.Hex(), "1", 12, 0)
	perms, _, err := MergeSkillGrantWithRemainder(current, &GrantRemainder{Native: big.NewInt(8)}, PolicyAddition{
		NativeSpendCap:   &model.NativeSpendCap{Amount: "5"},
		NativeRecipients: []*common.Address{&dest},
		ValidUntilMs:     now.Add(40 * 24 * time.Hour).UnixMilli(),
	}, []*avsproto.Task{sized}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if perms.NativeSpendCap == nil || perms.NativeSpendCap.Amount != "17" {
		t.Fatalf("native cap = %+v, want max(8, 12) + 5", perms.NativeSpendCap)
	}

	// No carried native cap, and the running payable value cannot be sized.
	// Do not install NativeTokenLimitModule.
	plain := usableGrant("01PLAIN", current.ValidUntil,
		[]model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		[]model.ERC20SpendCap{skillCap(skillUSDC, "10")})
	unsized := skillEthTask("eth", "Send ETH", dest.Hex(), "1", 0, 0)
	other := skillEthTask("eth2", "Other ETH", dest.Hex(), "1", 0, 0)
	_, _, err = MergeSkillGrant(plain, PolicyAddition{
		NativeSpendCap:   &model.NativeSpendCap{Amount: "5"},
		NativeRecipients: []*common.Address{&dest},
		AllowedActions:   []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		SpendCaps:        []model.ERC20SpendCap{skillCap(skillUSDC, "1")},
	}, []*avsproto.Task{unsized, other}, skillSepolia, now, time.Hour)
	var conflict *PolicyConflictError
	if !errors.As(err, &conflict) || conflict.Code != SessionPolicyNativeUnsizedCode || !errors.Is(err, ErrSessionNativeCapUnsized) || errors.Is(err, ErrSessionPolicyUnsized) {
		t.Fatalf("unsized native install = %v", err)
	}
	if len(conflict.AffectedTaskIDs) != 2 || conflict.AffectedTaskIDs[0] != "eth" || conflict.AffectedTaskIDs[1] != "eth2" || conflict.PolicyID != plain.ID {
		t.Fatalf("affected = %#v", conflict)
	}
	if !strings.Contains(conflict.Detail, "task eth (Send ETH)") || !strings.Contains(conflict.Detail, "task eth2 (Other ETH)") {
		t.Fatalf("detail = %v", conflict.Detail)
	}

	// A wallet that already has a native cap keeps the remainder.
	kept, _, err := MergeSkillGrantWithRemainder(current, &GrantRemainder{Native: big.NewInt(8)}, PolicyAddition{
		NativeSpendCap:   &model.NativeSpendCap{Amount: "5"},
		NativeRecipients: []*common.Address{&dest},
	}, []*avsproto.Task{unsized}, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatalf("existing native cap must survive an unsized payable, got %v", err)
	}
	if kept.NativeSpendCap == nil || kept.NativeSpendCap.Amount != "13" {
		t.Fatalf("kept native = %+v, want 8 + 5", kept.NativeSpendCap)
	}

	// A spent native cap is omitted, and so are its recipients.
	spentNative := usableGrant("01SPENT", current.ValidUntil, nil, nil)
	spentNative.NativeSpendCap = &model.NativeSpendCap{Amount: "9"}
	spentNative.NativeRecipients = []*common.Address{&dest}
	omitted, _, err := MergeSkillGrantWithRemainder(spentNative, &GrantRemainder{
		ERC20:  map[common.Address]*big.Int{},
		Native: big.NewInt(0),
	}, PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "1")},
	}, nil, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if omitted.NativeSpendCap != nil || len(omitted.NativeRecipients) != 0 {
		t.Fatalf("spent native cap was installed: %+v recipients %d", omitted.NativeSpendCap, len(omitted.NativeRecipients))
	}
}

func TestCarryCoverageRegressionUsesTheRemainder(t *testing.T) {
	now := skillNow()
	later := now.Add(60 * 24 * time.Hour).UnixMilli()
	usdc := common.HexToAddress(skillUSDC)
	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
	current := usableGrant("01LIVE", later,
		[]model.AllowedAction{
			skillAction(skillUSDC, selectorApprove),
			skillAction(skillRouter, selectorExactInputSingle),
		},
		[]model.ERC20SpendCap{skillCap(skillUSDC, "100")})
	rem := GrantRemainder{ERC20: map[common.Address]*big.Int{usdc: big.NewInt(8)}}

	// Schedule needs 12, chain has 8. The old usable cap is short, so a
	// replacement of 17 is not a regression.
	sched := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(1)), skillSepolia, 12, 0, later)
	// The pay task is a transfer. The current grant only approves. Use a
	// transfer grant for this case.
	transferGrant := usableGrant("01LIVE", later,
		[]model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		[]model.ERC20SpendCap{skillCap(skillUSDC, "100")})
	proposed := SessionPolicyInput{
		ChainID: skillSepolia,
		Permissions: SessionPermissions{
			AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
			SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "17")},
			ValidUntilMs:   later,
		},
	}
	if _, err := classifyCarriedCoverage(transferGrant, rem, proposed, []*avsproto.Task{sched}, now); err != nil {
		t.Fatalf("giving the schedule back must not be a regression: %v", err)
	}

	// Dropping the router is still worse. The swap's approve cap is covered.
	swap := swapSkillTask(t, 1)
	swap.ExpiredAt = later
	routerRem := GrantRemainder{ERC20: map[common.Address]*big.Int{usdc: big.NewInt(12)}}
	noRouter := SessionPolicyInput{
		ChainID: skillSepolia,
		Permissions: SessionPermissions{
			AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorApprove)},
			SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "12")},
			ValidUntilMs:   later,
		},
	}
	_, err := classifyCarriedCoverage(current, routerRem, noRouter, []*avsproto.Task{swap}, now)
	var conflict *PolicyConflictError
	if !errors.As(err, &conflict) || conflict.Code != SessionPolicyNotCoveringCode || conflict.PolicyID != current.ID {
		t.Fatalf("dropping the router = %#v", err)
	}

	// Remainder 0 is not coverage, so dropping the token is not a regression
	// even when a running transfer is unsized.
	unsized := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(1)), skillSepolia, 0, 0, 0)
	zero := GrantRemainder{ERC20: map[common.Address]*big.Int{usdc: big.NewInt(0)}}
	wethOnly := SessionPolicyInput{
		ChainID: skillSepolia,
		Permissions: SessionPermissions{
			AllowedActions: []model.AllowedAction{skillAction(skillWETH, selectorTransfer)},
			SpendCaps:      []model.ERC20SpendCap{skillCap(skillWETH, "5")},
			ValidUntilMs:   later,
		},
	}
	if _, err := classifyCarriedCoverage(transferGrant, zero, wethOnly, []*avsproto.Task{unsized}, now); err != nil {
		t.Fatalf("dropping a spent token must not freeze the wallet: %v", err)
	}

	// An unresolved task the current grant also cannot read does not freeze.
	split := skillWriteTask("split", "Split Incoming Payments", "{{value.tokenAddress}}", transferCalldata(payee, big.NewInt(1)), skillSepolia, 5, 0, 0)
	if _, err := classifyCarriedCoverage(transferGrant, GrantRemainder{ERC20: map[common.Address]*big.Int{usdc: big.NewInt(100)}}, proposed, []*avsproto.Task{split}, now); err != nil {
		t.Fatalf("unresolved task on a carried grant = %v", err)
	}

	// Each task fits, and the two of them together do not.
	one := skillWriteTask("a", "A", skillUSDC, transferCalldata(payee, big.NewInt(10)), skillSepolia, 1, 0, later)
	two := skillWriteTask("b", "B", skillUSDC, transferCalldata(payee, big.NewInt(10)), skillSepolia, 1, 0, later)
	short := SessionPolicyInput{
		ChainID: skillSepolia,
		Permissions: SessionPermissions{
			AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
			SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "15")},
			ValidUntilMs:   later,
		},
	}
	_, err = classifyCarriedCoverage(transferGrant, GrantRemainder{ERC20: map[common.Address]*big.Int{usdc: big.NewInt(100)}}, short, []*avsproto.Task{one, two}, now)
	if !errors.As(err, &conflict) || conflict.Detail != combinedSpendShortDetail || conflict.PolicyID != transferGrant.ID {
		t.Fatalf("combined shortfall = %#v", err)
	}
}

func TestExplainSkillDriftNamesRemainderAndRunningSet(t *testing.T) {
	now := skillNow()
	usdc := common.HexToAddress(skillUSDC)
	current := usableGrant("01OLD", now.Add(30*24*time.Hour).UnixMilli(),
		[]model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		[]model.ERC20SpendCap{skillCap(skillUSDC, "100")})
	addition := PolicyAddition{
		AllowedActions: []model.AllowedAction{skillAction(skillUSDC, selectorTransfer)},
		SpendCaps:      []model.ERC20SpendCap{skillCap(skillUSDC, "10")},
		ValidUntilMs:   now.Add(40 * 24 * time.Hour).UnixMilli(),
	}
	oldRem := &GrantRemainder{ERC20: map[common.Address]*big.Int{usdc: big.NewInt(60)}}
	snap := &skillPrepareSnapshot{
		chainID:    skillSepolia,
		addition:   addition,
		expiresIn:  time.Hour,
		preparedAt: now,
		remainder:  oldRem,
	}
	signed, _, err := MergeSkillGrantWithRemainder(current, oldRem, addition, nil, skillSepolia, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got := newBaseChanged(current); got.Detail != "the runner's usable grant changed; prepare again" || got.Code != SessionPolicyBaseChangedCode {
		t.Fatalf("id mismatch = %#v", got)
	}

	fresh := &GrantRemainder{ERC20: map[common.Address]*big.Int{usdc: big.NewInt(40)}}
	moved := explainSkillDrift(current, snap, fresh, nil, signed)
	if moved.Detail != "the remaining limit moved and the grant id did not; prepare again" || moved.PolicyID != current.ID {
		t.Fatalf("remainder = %#v", moved)
	}

	task := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(common.HexToAddress("0x0000000000000000000000000000000000000001"), big.NewInt(80)), skillSepolia, 1, 0, 0)
	setChanged := explainSkillDrift(current, snap, oldRem, []*avsproto.Task{task}, signed)
	if setChanged.Detail != "the running set changed and the grant id did not; prepare again" {
		t.Fatalf("running set = %#v", setChanged)
	}
	both := explainSkillDrift(current, snap, fresh, []*avsproto.Task{task}, signed)
	if both.Detail != "the remaining limit moved and the running set changed; prepare again" {
		t.Fatalf("both = %#v", both)
	}
	if got := explainSkillDrift(nil, snap, fresh, []*avsproto.Task{task}, signed); got.Detail != "the running set changed and the grant id did not; prepare again" {
		t.Fatalf("nil current = %#v", got)
	}
}

func TestSkillSubmitMergeErrorKeepsTheCauseWhenTheSetIsUnchanged(t *testing.T) {
	current := usableGrant("01OLD", time.Now().Add(time.Hour).UnixMilli(), nil, nil)
	unsized := fmt.Errorf("%w: cap overflows", ErrSessionPolicyUnsized)
	native := fmt.Errorf("%w: task pay (Pay): a native cap cannot be added while its payable value cannot be sized", ErrSessionNativeCapUnsized)
	same := []*avsproto.Task{skillWriteTask("pay", "Pay", skillUSDC, "0xa9059cbb", skillSepolia, 1, 0, 0)}
	reordered := []*avsproto.Task{
		skillWriteTask("other", "Other", skillUSDC, "0xa9059cbb", skillSepolia, 1, 0, 0),
		same[0],
	}
	prepared := []*avsproto.Task{reordered[1], reordered[0]}

	if err := skillSubmitMergeError(current, nil, same, same); err != nil {
		t.Fatalf("nil merge error = %v", err)
	}
	if err := skillSubmitMergeError(current, native, same, reordered); !errors.Is(err, ErrSessionNativeCapUnsized) {
		t.Fatalf("native refusal must keep its own error, got %v", err)
	}
	if err := skillSubmitMergeError(current, unsized, prepared, reordered); !errors.Is(err, ErrSessionPolicyUnsized) {
		t.Fatalf("same task ids must keep the merge error, got %v", err)
	}

	extra := append([]*avsproto.Task{}, same...)
	extra = append(extra, skillWriteTask("new", "New", skillUSDC, "0xa9059cbb", skillSepolia, 1, 0, 0))
	var conflict *PolicyConflictError
	if err := skillSubmitMergeError(current, unsized, same, extra); !errors.As(err, &conflict) || conflict.Detail != "the running set changed and the grant id did not; prepare again" {
		t.Fatalf("a new task = %#v", err)
	}
	if err := skillSubmitMergeError(current, unsized, extra, same); !errors.As(err, &conflict) || conflict.Code != SessionPolicyBaseChangedCode {
		t.Fatalf("a removed task = %#v", err)
	}
}

// limitScript answers erc20SpendLimits and records whether the runner lock
// was already held. TryRLock is used so a submit-side read cannot deadlock
// the test on the non-reentrant lock.
type limitScript struct {
	mu        sync.Mutex
	erc20     map[common.Address]*big.Int
	err       error
	lock      *sync.RWMutex
	calls     int
	tokens    []common.Address
	accounts  []common.Address
	entities  []uint32
	deadlines []time.Time
	writeHeld []bool
}

func (s *limitScript) CallContract(ctx context.Context, call ethereum.CallMsg, _ *big.Int) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	held := false
	if s.lock != nil {
		if s.lock.TryRLock() {
			s.lock.RUnlock()
		} else {
			held = true
		}
	}
	s.writeHeld = append(s.writeHeld, held)
	if dl, ok := ctx.Deadline(); ok {
		s.deadlines = append(s.deadlines, dl)
	}
	if s.err != nil {
		return nil, s.err
	}
	erc20Sel := crypto.Keccak256([]byte("erc20SpendLimits(uint32,address,address)"))[:4]
	if len(call.Data) < 100 || !bytes.Equal(call.Data[:4], erc20Sel) {
		return nil, fmt.Errorf("unexpected spend-limit call")
	}
	token := common.BytesToAddress(call.Data[48:68])
	account := common.BytesToAddress(call.Data[80:100])
	s.tokens = append(s.tokens, token)
	s.accounts = append(s.accounts, account)
	s.entities = append(s.entities, uint32(new(big.Int).SetBytes(call.Data[4:36]).Uint64()))
	amt := s.erc20[token]
	if amt == nil {
		amt = big.NewInt(0)
	}
	return common.LeftPadBytes(amt.Bytes(), 32), nil
}

func (s *limitScript) set(token common.Address, amt *big.Int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.erc20 == nil {
		s.erc20 = map[common.Address]*big.Int{}
	}
	s.erc20[token] = amt
}

func (s *limitScript) resetLog() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = 0
	s.tokens = nil
	s.accounts = nil
	s.entities = nil
	s.deadlines = nil
	s.writeHeld = nil
}

func (s *limitScript) snapshot() (tokens []common.Address, accounts []common.Address, entities []uint32, deadlines []time.Time, held []bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]common.Address{}, s.tokens...), append([]common.Address{}, s.accounts...), append([]uint32{}, s.entities...), append([]time.Time{}, s.deadlines...), append([]bool{}, s.writeHeld...)
}

func tokenPerms(token common.Address, amount string) SessionPermissions {
	return SessionPermissions{
		AllowedActions: []model.AllowedAction{{Target: &token, Selectors: []string{selectorTransfer}}},
		SpendCaps:      []model.ERC20SpendCap{{Token: &token, Amount: amount}},
		ValidUntilMs:   time.Now().Add(30 * 24 * time.Hour).UnixMilli(),
	}
}

func seedEnabledSkillTask(t *testing.T, db storage.Storage, owner, wallet common.Address, task *avsproto.Task) {
	t.Helper()
	task.Owner = owner.Hex()
	task.SmartWalletAddress = wallet.Hex()
	task.Status = avsproto.TaskStatus_Enabled
	wf := &model.Workflow{Task: task}
	body, err := wf.ToJSON()
	require.NoError(t, err)
	require.NoError(t, db.Set(WorkflowStorageKey(task.GetId(), avsproto.TaskStatus_Enabled), body))
	require.NoError(t, db.Set(TaskUserKey(wf), []byte(strconv.Itoa(int(avsproto.TaskStatus_Enabled)))))
}

func deleteEnabledSkillTask(t *testing.T, db storage.Storage, task *avsproto.Task) {
	t.Helper()
	wf := &model.Workflow{Task: task}
	require.NoError(t, db.Delete(TaskUserKey(wf)))
	require.NoError(t, db.Delete(WorkflowStorageKey(task.GetId(), avsproto.TaskStatus_Enabled)))
}

func submitLegacyPerms(t *testing.T, engine *Engine, key *ecdsa.PrivateKey, owner, wallet common.Address, perms SessionPermissions) *model.SessionPolicy {
	t.Helper()
	user := &model.User{Address: owner}
	prepared, err := engine.PrepareSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "Bot", Permissions: perms,
	})
	require.NoError(t, err)
	stored, _, err := engine.SubmitSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "Bot", Permissions: perms,
	}, prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline, signDigest(t, key, prepared.Digest))
	require.NoError(t, err)
	return stored
}

func skillAddition(token common.Address, amount string) PolicyAddition {
	return PolicyAddition{
		AllowedActions: []model.AllowedAction{{Target: &token, Selectors: []string{selectorTransfer}}},
		SpendCaps:      []model.ERC20SpendCap{{Token: &token, Amount: amount}},
		ValidUntilMs:   time.Now().Add(40 * 24 * time.Hour).UnixMilli(),
	}
}

func prepareSkill(t *testing.T, engine *Engine, owner, wallet common.Address, add PolicyAddition, base string) *PreparedSessionGrant {
	t.Helper()
	in := SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "Bot",
		Addition: &add, ExpiresInSeconds: 24 * 3600,
	}
	if base != "" {
		in.BasePolicyID = &base
	}
	prepared, err := engine.PrepareSessionPolicy(&model.User{Address: owner}, in)
	require.NoError(t, err)
	require.NotNil(t, prepared.EchoPermissions)
	return prepared
}

func TestSkillCarryChainReadLockAndDrift(t *testing.T) {
	engine, _, ownerKey, owner, wallet := newPolicyTestEngine(t)
	usdc := common.HexToAddress(skillUSDC)
	weth := common.HexToAddress(skillWETH)
	perms := SessionPermissions{
		AllowedActions: []model.AllowedAction{
			{Target: &usdc, Selectors: []string{selectorTransfer}},
			{Target: &weth, Selectors: []string{selectorTransfer}},
		},
		SpendCaps: []model.ERC20SpendCap{
			{Token: &usdc, Amount: "100"},
			{Token: &weth, Amount: "40"},
		},
		ValidUntilMs: time.Now().Add(30 * 24 * time.Hour).UnixMilli(),
	}
	stored := submitLegacyPerms(t, engine, ownerKey, owner, wallet, perms)
	require.NoError(t, MarkSessionGrantAppliedByID(engine.db, testPolicyChain, owner, wallet, stored.ID, "0xlanded"))

	script := &limitScript{lock: sessionAuthorityLock(testPolicyChain, owner, wallet)}
	script.set(usdc, big.NewInt(60))
	script.set(weth, big.NewInt(40))
	engine.spendLimitCallerOverride = func(int64) (aa.ContractCaller, error) { return script, nil }

	started := time.Now()
	add := skillAddition(usdc, "10")
	prepared := prepareSkill(t, engine, owner, wallet, add, stored.ID)
	got, ok := spendCapAmount(*prepared.EchoPermissions, usdc)
	require.True(t, ok)
	require.Equal(t, "70", got, "chain 60 plus the addition, not the stored 100")
	require.NotNil(t, prepared.SkillChanges)
	for _, line := range prepared.SkillChanges.Summary {
		require.NotContains(t, line, "was 100")
	}
	tokens, accounts, entities, deadlines, held := script.snapshot()
	require.Len(t, tokens, 2)
	require.ElementsMatch(t, []common.Address{usdc, weth}, tokens)
	for _, account := range accounts {
		require.Equal(t, wallet, account)
		require.NotEqual(t, owner, account)
	}
	for _, entity := range entities {
		require.Equal(t, stored.EntityID, entity)
	}
	require.False(t, held[0] || held[1], "prepare reads before the write lock")
	require.True(t, deadlines[0].Equal(deadlines[1]), "one context covers every token")
	span := deadlines[0].Sub(started)
	require.Greater(t, span, occupancyProbeTimeout-2*time.Second)
	require.Less(t, span, occupancyProbeTimeout+2*time.Second)

	script.resetLog()
	script.set(usdc, big.NewInt(40))
	_, err := submitEcho(t, engine, ownerKey, owner, wallet, prepared, stored.ID)
	var conflict *PolicyConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, SessionPolicyBaseChangedCode, conflict.Code)
	require.Equal(t, "the remaining limit moved and the grant id did not; prepare again", conflict.Detail)
	require.Equal(t, stored.ID, conflict.PolicyID)
	_, _, _, deadlines, held = script.snapshot()
	require.GreaterOrEqual(t, len(held), 2)
	require.True(t, held[0] && held[1], "submit re-reads while the write lock is held")
	require.True(t, deadlines[0].Equal(deadlines[1]))

	script.set(usdc, big.NewInt(60))
	script.resetLog()
	again := prepareSkill(t, engine, owner, wallet, add, stored.ID)
	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")
	seedEnabledSkillTask(t, engine.db, owner, wallet, skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(80)), skillSepolia, 1, 0, 0))
	_, err = submitEcho(t, engine, ownerKey, owner, wallet, again, stored.ID)
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, "the running set changed and the grant id did not; prepare again", conflict.Detail)
}

func submitEcho(t *testing.T, engine *Engine, key *ecdsa.PrivateKey, owner, wallet common.Address, prepared *PreparedSessionGrant, base string) (*model.SessionPolicy, error) {
	t.Helper()
	in := SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "Bot",
		Permissions: *prepared.EchoPermissions,
	}
	if base != "" {
		in.BasePolicyID = &base
	}
	stored, _, err := engine.SubmitSessionPolicy(&model.User{Address: owner}, in,
		prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline,
		signDigest(t, key, prepared.Digest))
	return stored, err
}

func TestSkillCarryUnappliedSkipsTheChain(t *testing.T) {
	engine, _, ownerKey, owner, wallet := newPolicyTestEngine(t)
	usdc := common.HexToAddress(skillUSDC)
	stored := submitLegacyPerms(t, engine, ownerKey, owner, wallet, tokenPerms(usdc, "100"))
	calls := 0
	engine.spendLimitCallerOverride = func(int64) (aa.ContractCaller, error) {
		calls++
		return nil, fmt.Errorf("reader must not be called")
	}
	prepared := prepareSkill(t, engine, owner, wallet, skillAddition(usdc, "10"), stored.ID)
	require.Zero(t, calls)
	got, ok := spendCapAmount(*prepared.EchoPermissions, usdc)
	require.True(t, ok)
	require.Equal(t, "110", got)

	require.NoError(t, MarkSessionGrantAppliedByID(engine.db, testPolicyChain, owner, wallet, stored.ID, "0xlanded"))
	engine.spendLimitCallerOverride = func(int64) (aa.ContractCaller, error) {
		return &limitScript{err: fmt.Errorf("rpc down")}, nil
	}
	_, err := engine.PrepareSessionPolicy(&model.User{Address: owner}, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "Bot",
		Addition: &[]PolicyAddition{skillAddition(usdc, "10")}[0], BasePolicyID: &stored.ID,
		ExpiresInSeconds: 3600,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "reading the remaining spend limit")
	require.NotContains(t, err.Error(), "110")
}

func TestSkillCarrySubmitCoverage(t *testing.T) {
	engine, db, ownerKey, owner, wallet := newPolicyTestEngine(t)
	user := &model.User{Address: owner}
	usdc := common.HexToAddress(skillUSDC)
	weth := common.HexToAddress(skillWETH)
	payee := common.HexToAddress("0x0000000000000000000000000000000000000001")

	// No current grant: an enabled task the new grant does not cover is
	// still refused.
	pay := skillWriteTask("pay", "Pay", skillUSDC, transferCalldata(payee, big.NewInt(1)), skillSepolia, 1, 0, 0)
	seedEnabledSkillTask(t, db, owner, wallet, pay)
	wethPerms := tokenPerms(weth, "10")
	prepared, err := engine.PrepareSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "Bot", Permissions: wethPerms,
	})
	require.NoError(t, err)
	_, _, err = engine.SubmitSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "Bot", Permissions: wethPerms,
	}, prepared.Policy.ID, prepared.Policy.EntityID, prepared.Policy.Grant.Deadline, signDigest(t, ownerKey, prepared.Digest))
	var conflict *PolicyConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, SessionPolicyNotCoveringCode, conflict.Code)
	deleteEnabledSkillTask(t, db, pay)

	// No current grant: an unresolved task still fails prepare.
	split := skillWriteTask("split", "Split Incoming Payments", "{{value.tokenAddress}}", transferCalldata(payee, big.NewInt(1)), skillSepolia, 5, 0, 0)
	seedEnabledSkillTask(t, db, owner, wallet, split)
	_, err = engine.PrepareSessionPolicy(user, SessionPolicyInput{
		Wallet: wallet, ChainID: testPolicyChain, AgentLabel: "Bot",
		Addition: &[]PolicyAddition{skillAddition(usdc, "10")}[0], ExpiresInSeconds: 3600,
	})
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, SessionPolicyTargetUnresolvedCode, conflict.Code)
	deleteEnabledSkillTask(t, db, split)

	stored := submitLegacyPerms(t, engine, ownerKey, owner, wallet, tokenPerms(usdc, "100"))
	seedEnabledSkillTask(t, db, owner, wallet, split)
	carried := prepareSkill(t, engine, owner, wallet, skillAddition(usdc, "1"), stored.ID)
	_, err = submitEcho(t, engine, ownerKey, owner, wallet, carried, stored.ID)
	require.NoError(t, err)

	// Unsized transfer on a token this grant never capped. Prepare and
	// submit succeed, and the token is not added.
	unsizedWETH := skillWriteTask("weth", "Wrap", skillWETH, transferCalldata(payee, big.NewInt(1)), skillSepolia, 0, 0, 0)
	seedEnabledSkillTask(t, db, owner, wallet, unsizedWETH)
	current, err := ActiveSessionPolicyForWallet(db, testPolicyChain, owner, wallet)
	require.NoError(t, err)
	require.NotNil(t, current)
	next := prepareSkill(t, engine, owner, wallet, skillAddition(usdc, "1"), current.ID)
	_, ok := spendCapAmount(*next.EchoPermissions, weth)
	require.False(t, ok)
	require.False(t, actionHasSelector(next.EchoPermissions.AllowedActions, skillWETH, selectorTransfer))
	stored, err = submitEcho(t, engine, ownerKey, owner, wallet, next, current.ID)
	require.NoError(t, err)

	// The carried USDC remainder is 0 and a running transfer of it is
	// unsized. Submit succeeds and the token is dropped.
	require.NoError(t, MarkSessionGrantAppliedByID(db, testPolicyChain, owner, wallet, stored.ID, "0xlanded"))
	script := &limitScript{}
	script.set(usdc, big.NewInt(0))
	engine.spendLimitCallerOverride = func(int64) (aa.ContractCaller, error) { return script, nil }
	unsizedUSDC := skillWriteTask("usdc", "Send", skillUSDC, transferCalldata(payee, big.NewInt(1)), skillSepolia, 0, 0, 0)
	seedEnabledSkillTask(t, db, owner, wallet, unsizedUSDC)
	applied, err := ActiveSessionPolicyForWallet(db, testPolicyChain, owner, wallet)
	require.NoError(t, err)
	dropped := prepareSkill(t, engine, owner, wallet, skillAddition(weth, "5"), applied.ID)
	_, ok = spendCapAmount(*dropped.EchoPermissions, usdc)
	require.False(t, ok, "a zero remainder must not be installed")
	got, ok := spendCapAmount(*dropped.EchoPermissions, weth)
	require.True(t, ok)
	require.Equal(t, "5", got)
	tokens, _, _, _, _ := script.snapshot()
	require.NotEmpty(t, tokens)
	for _, token := range tokens {
		require.Equal(t, usdc, token, "a token the grant never capped must not be read")
	}
	_, err = submitEcho(t, engine, ownerKey, owner, wallet, dropped, applied.ID)
	require.NoError(t, err)
}

func skillPrepareTestSnap(owner, wallet common.Address) skillPrepareSnapshot {
	return skillPrepareSnapshot{owner: owner, wallet: wallet, chainID: skillSepolia, preparedAt: skillNow()}
}

func skillPrepareAddr(n int) common.Address {
	return common.HexToAddress(fmt.Sprintf("0x%040x", n))
}

func assertSkillPrepareConsistent(t *testing.T, c *skillPrepareCache) {
	t.Helper()
	require.NotNil(t, c)
	require.Equal(t, len(c.byID), c.bySaved.Len())
	seen := map[string]bool{}
	for i, node := range c.bySaved {
		require.NotNil(t, node)
		require.Equal(t, i, node.index)
		require.Equal(t, node, c.byID[node.id])
		require.False(t, seen[node.id])
		seen[node.id] = true
		for _, child := range []int{i*2 + 1, i*2 + 2} {
			if child >= len(c.bySaved) {
				continue
			}
			require.False(t, c.bySaved.Less(child, i), "heap property broken at %d", i)
		}
	}
	runners := map[string]int{}
	for key, ids := range c.byRunner {
		require.LessOrEqual(t, len(ids), skillPrepareMaxPerRunner)
		for _, id := range ids {
			require.Contains(t, c.byID, id)
			runners[id]++
			node := c.byID[id]
			require.Equal(t, key, skillPrepareRunnerKey(node.snap.owner, node.snap.wallet, node.snap.chainID))
		}
	}
	require.Equal(t, len(c.byID), len(runners))
	owners := map[string]int{}
	for key, ids := range c.byOwner {
		require.LessOrEqual(t, len(ids), skillPrepareMaxPerOwner)
		for _, id := range ids {
			require.Contains(t, c.byID, id)
			owners[id]++
			require.Equal(t, key, skillPrepareOwnerKey(c.byID[id].snap.owner))
		}
	}
	require.Equal(t, len(c.byID), len(owners))
}

func TestSkillPrepareCacheIsBounded(t *testing.T) {
	owner := skillPrepareAddr(1)
	wallet := skillPrepareAddr(2)
	otherOwner := skillPrepareAddr(3)
	otherWallet := skillPrepareAddr(4)

	t.Run("per runner", func(t *testing.T) {
		engine := &Engine{}
		engine.rememberSkillPrepare("other", skillPrepareTestSnap(otherOwner, otherWallet))
		const extra = 3
		newest := ""
		for i := 0; i < skillPrepareMaxPerRunner+extra; i++ {
			id := fmt.Sprintf("runner-%04d", i)
			engine.rememberSkillPrepare(id, skillPrepareTestSnap(owner, wallet))
			newest = id
		}
		for i := 0; i < extra; i++ {
			require.Nil(t, engine.skillPrepareFor(fmt.Sprintf("runner-%04d", i)))
		}
		require.NotNil(t, engine.skillPrepareFor(newest))
		require.NotNil(t, engine.skillPrepareFor("other"))
		runnerKey := skillPrepareRunnerKey(owner, wallet, skillSepolia)
		require.Len(t, engine.skillPrepare.byRunner[runnerKey], skillPrepareMaxPerRunner)
		assertSkillPrepareConsistent(t, engine.skillPrepare)
	})

	t.Run("per owner", func(t *testing.T) {
		engine := &Engine{}
		engine.rememberSkillPrepare("other", skillPrepareTestSnap(otherOwner, otherWallet))
		wallets := skillPrepareMaxPerOwner/skillPrepareMaxPerRunner + 1
		var first, last string
		for w := 0; w < wallets; w++ {
			for i := 0; i < skillPrepareMaxPerRunner; i++ {
				id := fmt.Sprintf("owner-%02d-%02d", w, i)
				engine.rememberSkillPrepare(id, skillPrepareTestSnap(owner, skillPrepareAddr(100+w)))
				if first == "" {
					first = id
				}
				last = id
			}
		}
		require.Nil(t, engine.skillPrepareFor(first))
		require.NotNil(t, engine.skillPrepareFor(last))
		require.NotNil(t, engine.skillPrepareFor("other"))
		require.LessOrEqual(t, len(engine.skillPrepare.byOwner[skillPrepareOwnerKey(owner)]), skillPrepareMaxPerOwner)
		assertSkillPrepareConsistent(t, engine.skillPrepare)
	})

	t.Run("global", func(t *testing.T) {
		engine := &Engine{}
		owners := skillPrepareMaxEntries / skillPrepareMaxPerOwner
		runners := skillPrepareMaxPerOwner / skillPrepareMaxPerRunner
		var oldest string
		for o := 0; o < owners; o++ {
			for w := 0; w < runners; w++ {
				for i := 0; i < skillPrepareMaxPerRunner; i++ {
					id := fmt.Sprintf("g-%02d-%02d-%02d", o, w, i)
					engine.rememberSkillPrepare(id, skillPrepareTestSnap(skillPrepareAddr(1000+o), skillPrepareAddr(2000+w)))
					if oldest == "" {
						oldest = id
					}
				}
			}
		}
		require.Len(t, engine.skillPrepare.byID, skillPrepareMaxEntries)
		engine.rememberSkillPrepare("overflow", skillPrepareTestSnap(skillPrepareAddr(9000), skillPrepareAddr(9001)))
		require.Nil(t, engine.skillPrepareFor(oldest))
		require.NotNil(t, engine.skillPrepareFor("overflow"))
		require.LessOrEqual(t, len(engine.skillPrepare.byID), skillPrepareMaxEntries)
		assertSkillPrepareConsistent(t, engine.skillPrepare)
	})

	t.Run("forget and replace", func(t *testing.T) {
		engine := &Engine{}
		engine.rememberSkillPrepare("same", skillPrepareTestSnap(owner, wallet))
		engine.rememberSkillPrepare("SAME", skillPrepareTestSnap(owner, wallet))
		require.Len(t, engine.skillPrepare.byID, 1)
		require.NotNil(t, engine.skillPrepareFor("same"))
		engine.forgetSkillPrepare("same")
		require.Nil(t, engine.skillPrepareFor("same"))
		require.Empty(t, engine.skillPrepare.byID)
		require.Zero(t, engine.skillPrepare.bySaved.Len())
		assertSkillPrepareConsistent(t, engine.skillPrepare)
	})

	t.Run("expired prefix", func(t *testing.T) {
		engine := &Engine{}
		engine.rememberSkillPrepare("keep", skillPrepareTestSnap(owner, wallet))
		engine.rememberSkillPrepare("stale", skillPrepareTestSnap(owner, wallet))
		engine.skillPrepareMu.Lock()
		node := engine.skillPrepare.byID["stale"]
		require.NotNil(t, node)
		node.savedAt = time.Now().Add(-skillPrepareTTL - time.Second)
		node.snap.savedAt = node.savedAt
		heap.Fix(&engine.skillPrepare.bySaved, node.index)
		engine.skillPrepareMu.Unlock()
		engine.rememberSkillPrepare("fresh", skillPrepareTestSnap(owner, wallet))
		_, staleKept := engine.skillPrepare.byID["stale"]
		require.False(t, staleKept)
		require.NotNil(t, engine.skillPrepareFor("keep"))
		require.NotNil(t, engine.skillPrepareFor("fresh"))
		assertSkillPrepareConsistent(t, engine.skillPrepare)
	})

	t.Run("zero engine", func(t *testing.T) {
		var engine Engine
		engine.forgetSkillPrepare("missing")
		require.Nil(t, engine.skillPrepareFor("missing"))
		engine.rememberSkillPrepare("", skillPrepareTestSnap(owner, wallet))
		require.Nil(t, engine.skillPrepare)
		engine.rememberSkillPrepare("one", skillPrepareTestSnap(owner, wallet))
		require.NotNil(t, engine.skillPrepareFor("one"))
	})
}
