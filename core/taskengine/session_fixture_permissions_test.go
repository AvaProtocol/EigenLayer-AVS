package taskengine

import (
	"context"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// Live-chain fixtures run against long-lived Sepolia wallets, so a test's
// runner carries whatever previous runs — and Studio, and spikes — left behind.
// The permission state on that wallet is the fixture, and it has to be checked
// like one.
//
// The bug this file exists to remove: the old check asked "is a signer present
// on this entity" and adopted the entity when the answer was yes. The question
// it needed to ask is "does this entity grant what my test needs". Those come
// apart exactly when it matters. On the shared runner
// 0x209eb31c199bEB4c386eF83CF442DE1a00667a1F, entity 1 names the controller —
// so the old check passed — while actually carrying an AllowlistModule
// execution hook and a TimeRangeModule validation hook from a Studio-shaped
// grant. A test modelling that entity as a bare hookless grant then builds an
// operation the account rejects at validation: the allowlist does not cover the
// call, and the stored grant says RequiresExecuteUserOp=false while the real
// entity's execution hook requires every operation under it to be
// executeUserOp-wrapped. Either alone is an opaque AA23 at estimation.
//
// Why not uninstall the stale entity and reinstall the right one: revoking a
// grant needs each hook module's onUninstall payload, and the AllowlistModule's
// is the same (entityId, inputs) tuple it was installed with — recoverable only
// from the stored install call (see core/chainio/aa/ma_v2_uninstall.go, which
// is explicit that it must be decoded from the stored call rather than rebuilt).
// A fixture wallet's stale entities were installed by runs whose database is
// long gone, so that payload cannot be reconstructed, and uninstalling without
// it strands module state instead of clearing it. Skipping to a free entity is
// what is actually available.

// hookRef is one installed hook: which module, at which entity, registered for
// which callbacks. The flags matter — they select validation versus execution
// and, for execution hooks, pre versus post — so an allowlist registered
// post-only is not the same authorization as the same module registered as a
// pre-execution hook, and must not compare equal to it.
type hookRef struct {
	Module common.Address
	Entity uint32
	Flags  byte
}

func (h hookRef) String() string {
	return fmt.Sprintf("%s#%d/0x%02x", h.Module.Hex(), h.Entity, h.Flags)
}

// validationState is an account's answer for one validation entity.
type validationState struct {
	// Installed is false when the entity names no signer — free to claim.
	Installed bool
	Signer    common.Address

	// Flags is the validation's own configuration: which of UserOp /
	// Signature / Global authority it carries (aa.ValidationFlag*).
	Flags byte

	ValidationHooks []hookRef
	ExecutionHooks  []hookRef
	SelectorCount   int
}

// expectedPermissions is the state a live test needs on its runner before it
// can build an operation the account will accept. This is the "expected
// permission object" a fixture declares up front.
//
// Hook order is not compared: the account stores hooks as a prepend-on-add
// list, so install order and stored order differ by design.
type expectedPermissions struct {
	Signer          common.Address
	Flags           byte
	ValidationHooks []hookRef
	ExecutionHooks  []hookRef
	// Global means the validation carries no selector scoping.
	Global bool

	// Reusable allows an entity in exactly this state to be adopted instead of
	// stepped over.
	//
	// It is false whenever the expected state includes a STATEFUL hook, and
	// that is the whole point. A HookConfig names a module and an entity; it
	// says nothing about the configuration that module holds for that entity.
	// Two grants can present an identical allowlist HookConfig while one
	// permits approve() on USDC only and the other on USDC and WETH, and
	// nothing readable from the account distinguishes them. Adopting on a
	// HookConfig match would reintroduce exactly the stale-allowlist AA23 that
	// #714 fixed by always installing fresh.
	Reusable bool
}

// bareGlobalGrant is the fixture shape grantControllerAuthority installs: a
// global grant with no hooks at all.
//
// Reusable, and it is the only shape that safely can be: "no hooks" is a
// complete description. There is no module holding configuration behind it, so
// a match here is a match on everything that decides whether an operation
// validates.
func bareGlobalGrant(controller common.Address) expectedPermissions {
	return expectedPermissions{
		Signer:   controller,
		Flags:    aa.ValidationFlagUserOp | aa.ValidationFlagGlobal,
		Global:   true,
		Reusable: true,
	}
}

// hookedGlobalGrant is the production-shaped fixture: allowlist + time range as
// validation hooks, allowlist again as the pre-execution hook that makes the
// grant executeUserOp-wrapped.
//
// NOT reusable — see expectedPermissions.Reusable. This shape describes which
// modules are installed, not what they were configured to permit, so a
// matching entity is stepped over and a fresh one is installed. That keeps
// #714's behaviour while giving its failures a readable diff.
func hookedGlobalGrant(controller common.Address, entity uint32) expectedPermissions {
	allowlist := func(flags byte) hookRef {
		return hookRef{Module: aa.AllowlistModuleAddress(), Entity: entity, Flags: flags}
	}
	return expectedPermissions{
		Signer: controller,
		Flags:  aa.ValidationFlagUserOp | aa.ValidationFlagGlobal,
		ValidationHooks: []hookRef{
			allowlist(aa.HookFlagValidation),
			{Module: aa.TimeRangeModuleAddress(), Entity: entity, Flags: aa.HookFlagValidation},
		},
		ExecutionHooks: []hookRef{allowlist(aa.HookFlagExecHasPre)},
		Global:         true,
	}
}

// diff reports why an on-chain entity is not the state a fixture asked for, or
// "" when it is. The message names both sides: a fixture failure that only says
// "mismatch" sends the reader to a block explorer.
func (want expectedPermissions) diff(got validationState) string {
	if !got.Installed {
		return "entity is free (no signer installed)"
	}
	var problems []string
	if got.Signer != want.Signer {
		problems = append(problems, fmt.Sprintf("signer is %s, want %s", got.Signer.Hex(), want.Signer.Hex()))
	}
	if got.Flags != want.Flags {
		// An entity without ValidationFlagGlobal authorizes no global call, and
		// one with ValidationFlagSignature carries signing authority this
		// fixture never asked for. Neither is visible in the selector list.
		problems = append(problems, fmt.Sprintf("validation flags are 0x%02x, want 0x%02x", got.Flags, want.Flags))
	}
	if d := hookDiff("validation hooks", want.ValidationHooks, got.ValidationHooks); d != "" {
		problems = append(problems, d)
	}
	if d := hookDiff("execution hooks", want.ExecutionHooks, got.ExecutionHooks); d != "" {
		problems = append(problems, d)
	}
	if want.Global && got.SelectorCount != 0 {
		problems = append(problems, fmt.Sprintf("validation is scoped to %d selector(s), want global", got.SelectorCount))
	}
	return strings.Join(problems, "; ")
}

// hookDiff compares two hook sets as sets.
func hookDiff(label string, want, got []hookRef) string {
	key := func(hooks []hookRef) []string {
		out := make([]string, 0, len(hooks))
		for _, h := range hooks {
			out = append(out, h.String())
		}
		sort.Strings(out)
		return out
	}
	wantKeys, gotKeys := key(want), key(got)
	if strings.Join(wantKeys, ",") == strings.Join(gotKeys, ",") {
		return ""
	}
	return fmt.Sprintf("%s are [%s], want [%s]",
		label, strings.Join(gotKeys, " "), strings.Join(wantKeys, " "))
}

// matches reports whether the entity is exactly the fixture's expected state.
func (want expectedPermissions) matches(got validationState) bool { return want.diff(got) == "" }

var (
	validationDataOnce sync.Once
	validationDataABI  abi.ABI
	validationDataErr  error
)

// ensureValidationDataABI parses IModularAccountView.getValidationData.
//
// The return is ValidationDataView: a packed flags byte, the two hook lists as
// bytes25 HookConfigs (module ++ entityId ++ flags), and the selector scoping.
func ensureValidationDataABI() error {
	validationDataOnce.Do(func() {
		validationDataABI, validationDataErr = abi.JSON(strings.NewReader(`[
		  {"type":"function","name":"getValidationData","stateMutability":"view",
		   "inputs":[{"name":"validationFunction","type":"bytes24"}],
		   "outputs":[{"name":"","type":"tuple","components":[
		     {"name":"validationFlags","type":"uint8"},
		     {"name":"validationHooks","type":"bytes25[]"},
		     {"name":"executionHooks","type":"bytes25[]"},
		     {"name":"selectors","type":"bytes4[]"}]}]}]`))
	})
	return validationDataErr
}

// decodeValidationData turns a getValidationData return into a validationState.
// The signer is not in this payload — it lives on the validation module — so
// the caller fills it in.
func decodeValidationData(raw []byte) (validationState, error) {
	if err := ensureValidationDataABI(); err != nil {
		return validationState{}, err
	}
	values, err := validationDataABI.Methods["getValidationData"].Outputs.Unpack(raw)
	if err != nil {
		return validationState{}, fmt.Errorf("decoding getValidationData: %w", err)
	}
	if len(values) != 1 {
		return validationState{}, fmt.Errorf("getValidationData returned %d values, want 1", len(values))
	}
	// The generated anonymous struct is reachable by field name only through
	// reflection; re-marshalling through the ABI type is simpler to read.
	view, ok := values[0].(struct {
		ValidationFlags uint8       `json:"validationFlags"`
		ValidationHooks [][25]uint8 `json:"validationHooks"`
		ExecutionHooks  [][25]uint8 `json:"executionHooks"`
		Selectors       [][4]uint8  `json:"selectors"`
	})
	if !ok {
		return validationState{}, fmt.Errorf("getValidationData returned %T, not a ValidationDataView", values[0])
	}
	return validationState{
		Flags:           view.ValidationFlags,
		ValidationHooks: hookRefsFrom(view.ValidationHooks),
		ExecutionHooks:  hookRefsFrom(view.ExecutionHooks),
		SelectorCount:   len(view.Selectors),
	}, nil
}

// hookRefsFrom unpacks bytes25 HookConfigs: module (20) ++ entityId (4) ++ flags (1).
func hookRefsFrom(configs [][25]uint8) []hookRef {
	out := make([]hookRef, 0, len(configs))
	for _, c := range configs {
		entity := uint32(c[20])<<24 | uint32(c[21])<<16 | uint32(c[22])<<8 | uint32(c[23])
		out = append(out, hookRef{Module: common.BytesToAddress(c[:20]), Entity: entity, Flags: c[24]})
	}
	return out
}

// readValidationState reads one entity's full permission state off the chain.
//
// Two calls, because they live in different places: the signer is per-entity
// state on the SingleSignerValidationModule, while the hooks and selector
// scoping are the account's own view of the validation.
func readValidationState(
	t *testing.T,
	swCfg *config.SmartWalletConfig,
	wallet common.Address,
	entity uint32,
) validationState {
	t.Helper()

	// A read failure must never present as a free entity: claiming an entity
	// that is actually occupied installs a second validation over the first.
	// Only a successful zero answer means free.
	signer, err := entitySignerOnChain(t, swCfg, wallet, entity)
	if err != nil {
		t.Fatalf("reading the signer of entity %d on %s: %v", entity, wallet.Hex(), err)
	}
	if signer == (common.Address{}) {
		return validationState{Installed: false}
	}

	client, err := ethclient.Dial(swCfg.EthRpcUrl)
	if err != nil {
		t.Fatalf("dialing the chain to read validation state: %v", err)
	}
	defer client.Close()

	if err := ensureValidationDataABI(); err != nil {
		t.Fatalf("building the getValidationData ABI: %v", err)
	}
	moduleEntity := aa.PackModuleEntity(aa.SingleSignerValidationModuleAddress(), entity)
	data, err := validationDataABI.Pack("getValidationData", moduleEntity)
	if err != nil {
		t.Fatalf("packing getValidationData: %v", err)
	}
	out, err := client.CallContract(context.Background(), ethereum.CallMsg{To: &wallet, Data: data}, nil)
	if err != nil {
		// A signer exists but the account cannot describe the validation.
		// Treat it as installed-and-unknown rather than free: claiming this
		// entity would collide with whatever is there.
		t.Logf("fixture: entity %d on %s has signer %s but getValidationData failed (%v); treating as occupied",
			entity, wallet.Hex(), signer.Hex(), err)
		return validationState{Installed: true, Signer: signer}
	}
	state, err := decodeValidationData(out)
	if err != nil {
		t.Fatalf("reading validation state for entity %d on %s: %v", entity, wallet.Hex(), err)
	}
	state.Installed = true
	state.Signer = signer
	return state
}

// maxFixtureEntityScan bounds how far a fixture will walk looking for an entity
// it can use. Every skipped entity is one a previous run left installed and
// nothing can clean up, so a wallet that needs this many is a wallet to
// investigate, not to keep piling onto.
const maxFixtureEntityScan = 16

// Fixture runner salts.
//
// Every live fixture derives its runner from salt 0 today, which is why one
// test's leftover validation entities become another test's problem: the wallet
// is shared, and the entity layout on it belongs to whichever test installed
// first. Verifying permissions (above) removes the failure mode; separate salts
// would remove the coupling.
//
// They are all still 0 because a new salt is an undeployed, unfunded
// counterfactual wallet. Flipping one without funding its runner turns a
// passing live test into a failing one, so the salt and the funding have to
// move together: pick a fresh value here, run the test once to have
// requireFundedRunner print the runner address and the shortfall, fund it, and
// it stays isolated from then on.
const (
	fixtureSaltWithdrawal        = 0
	fixtureSaltSequentialWrites  = 0
	fixtureSaltUniswapSimulation = 0
	fixtureSaltBatchSwap         = 0
	// Salt 13 is funded and its entity space is clean, so unlike the fixtures
	// above this one is genuinely isolated. The replace check installs and
	// removes entities; doing that on a shared runner would churn another
	// test's entity layout.
	fixtureSaltGrantReplace = 13
)

// resetInitialPermissions brings the fixture's runner to the permission state
// the test declares, and returns the stored grant to run under.
//
// "Reset" here means converge, not wipe. An entity already carrying exactly the
// expected state is adopted as-is (its stored grant is marked applied, so the
// install is not replayed onto an entity that already exists); an entity
// carrying anything else is stepped over; the first free entity is claimed and
// its install rides the test's first operation. Wiping would need each stale
// hook's onUninstall payload, which is not recoverable for entities installed
// by runs whose database is gone — see the file comment.
//
// want is a function of the entity because hook installs embed the entity id,
// so "the same grant" at entity 2 is a different expected state than at 1.
//
// prepare builds the grant payload for one attempt; the loop owns entity
// allocation. Stepping over an occupied entity means storing that attempt as
// revoked, which is what advances NextSessionEntityID to the next candidate.
//
// The steady state on a wallet used by one fixture is a single entity: run one
// installs it, every later run matches and adopts it. Entities accumulate only
// when a fixture's expected shape changes — a code change, not a per-run cost.
func resetInitialPermissions(
	t *testing.T,
	db storage.Storage,
	swCfg *config.SmartWalletConfig,
	wallet common.Address,
	want func(entity uint32) expectedPermissions,
	prepare func(policyID string) (*PreparedSessionGrant, error),
) *model.SessionPolicy {
	t.Helper()

	ownerKey := requireOwnerKey(t)
	skipped := make([]string, 0, 4)

	for attempt := 0; attempt < maxFixtureEntityScan; attempt++ {
		prepared, err := prepare(fmt.Sprintf("test-fixture-grant-%d", attempt))
		if err != nil {
			t.Fatalf("preparing the fixture grant (attempt %d): %v", attempt, err)
		}
		sig, err := crypto.Sign(prepared.Digest.Bytes(), ownerKey) // raw digest
		if err != nil {
			t.Fatalf("signing the fixture grant: %v", err)
		}
		sig[64] += 27

		policy, err := SubmitSessionGrant(db, prepared, sig)
		if err != nil {
			t.Fatalf("storing the fixture grant (attempt %d): %v", attempt, err)
		}

		state := readValidationState(t, swCfg, wallet, policy.EntityID)
		switch {
		case !state.Installed:
			t.Logf("fixture: entity %d on %s is free; the install rides the first operation",
				policy.EntityID, wallet.Hex())
			return policy

		case want(policy.EntityID).Reusable && want(policy.EntityID).matches(state):
			if err := MarkSessionGrantApplied(db, policy, "pre-existing"); err != nil {
				t.Fatalf("adopting the installed grant at entity %d: %v", policy.EntityID, err)
			}
			t.Logf("fixture: entity %d on %s already grants exactly what this test needs; reusing it",
				policy.EntityID, wallet.Hex())
			return policy

		default:
			// Occupied by a grant this fixture cannot use. Retire the stored
			// attempt so the allocator moves on, and remember why for the
			// failure message if nothing works out.
			reason := want(policy.EntityID).diff(state)
			if reason == "" {
				reason = "the modules match but their configuration is not readable from the account, " +
					"so this fixture installs fresh rather than trusting a hook set it cannot inspect"
			}
			policy.Status = model.SessionPolicyRevoked
			if err := StoreSessionPolicy(db, policy); err != nil {
				t.Fatalf("stepping over occupied entity %d: %v", policy.EntityID, err)
			}
			skipped = append(skipped, fmt.Sprintf("entity %d (%s)", policy.EntityID, reason))
			t.Logf("fixture: entity %d on %s is not usable — %s; trying the next one",
				policy.EntityID, wallet.Hex(), reason)
		}
	}

	t.Fatalf("no usable session entity on %s after %d attempts: %s.\n"+
		"Every entity carries permissions this fixture cannot use, and stale entities cannot be "+
		"uninstalled without their original hook install payloads. Give this fixture its own salt, "+
		"or clean the wallet up manually.",
		wallet.Hex(), maxFixtureEntityScan, strings.Join(skipped, "; "))
	return nil
}

// requireFundedRunner fails the test with an actionable message when the runner
// cannot cover what the test is about to move.
//
// Live fixtures hard-fail on missing prerequisites rather than skipping: a
// silent skip on an unfunded wallet reads as a passing suite that never
// exercised the path. This is also what makes a per-fixture salt safe to
// introduce — a fresh wallet's first failure names itself and the shortfall
// instead of surfacing as an opaque estimation revert.
func requireFundedRunner(
	t *testing.T,
	swCfg *config.SmartWalletConfig,
	wallet common.Address,
	need *big.Int,
) {
	t.Helper()
	client, err := ethclient.Dial(swCfg.EthRpcUrl)
	if err != nil {
		t.Fatalf("dialing the chain to check the runner's balance: %v", err)
	}
	defer client.Close()

	balance, err := client.BalanceAt(context.Background(), wallet, nil)
	if err != nil {
		t.Fatalf("reading the balance of %s: %v", wallet.Hex(), err)
	}
	if balance.Cmp(need) < 0 {
		short := new(big.Int).Sub(need, balance)
		t.Fatalf("runner %s holds %s wei but this test needs %s wei — fund it with at least %s wei (%.6f ETH) and re-run",
			wallet.Hex(), balance.String(), need.String(), short.String(),
			new(big.Float).Quo(new(big.Float).SetInt(short), big.NewFloat(1e18)))
	}
}

// requireFixtureBalance is requireFundedRunner for balances that are not the
// runner's native ETH — an ERC-20 holding, an EntryPoint deposit — where the
// caller has already read the number and only needs the same hard-fail rule
// applied to it.
func requireFixtureBalance(t *testing.T, what string, holder common.Address, have, need *big.Int) {
	t.Helper()
	if have.Cmp(need) < 0 {
		t.Fatalf("%s of %s is %s but this test needs %s — top it up by %s and re-run",
			what, holder.Hex(), have.String(), need.String(),
			new(big.Int).Sub(need, have).String())
	}
}
