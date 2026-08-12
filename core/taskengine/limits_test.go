package taskengine

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/testutil"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func TestMinCronInterval(t *testing.T) {
	tests := []struct {
		name string
		expr string
		want time.Duration
	}{
		{"every minute", "* * * * *", time.Minute},
		{"every 5 minutes", "*/5 * * * *", 5 * time.Minute},
		{"every 6 hours", "0 */6 * * *", 6 * time.Hour},
		{"hourly", "0 * * * *", time.Hour},
		{"daily", "0 0 * * *", 24 * time.Hour},
		// The tightest gap is not the first gap: this fires at :00 and :01,
		// then waits ~59m. A naive "measure the next two fires" check reads
		// 60s or 3540s depending on when it runs.
		{"tight pair inside a sparse hour", "0,1 * * * *", time.Minute},
		// cron.Descriptor accepts @every, which pattern-matching the
		// expression text would miss entirely.
		{"descriptor every second", "@every 1s", time.Second},
		{"descriptor hourly", "@hourly", time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := minCronInterval(tt.expr)
			if err != nil {
				t.Fatalf("minCronInterval(%q) returned error: %v", tt.expr, err)
			}
			if got != tt.want {
				t.Errorf("minCronInterval(%q) = %v, want %v", tt.expr, got, tt.want)
			}
		})
	}
}

func TestMinCronIntervalRejectsGarbage(t *testing.T) {
	if _, err := minCronInterval("not a cron"); err == nil {
		t.Error("expected an error for an unparseable schedule, got nil")
	}
}

func cronTrigger(schedules ...string) *avsproto.TaskTrigger {
	return &avsproto.TaskTrigger{
		Type: avsproto.TriggerType_TRIGGER_TYPE_CRON,
		TriggerType: &avsproto.TaskTrigger_Cron{
			Cron: &avsproto.CronTrigger{
				Config: &avsproto.CronTrigger_Config{Schedules: schedules},
			},
		},
	}
}

func blockTrigger(interval, chainID int64) *avsproto.TaskTrigger {
	return &avsproto.TaskTrigger{
		Type: avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		TriggerType: &avsproto.TaskTrigger_Block{
			Block: &avsproto.BlockTrigger{
				Config: &avsproto.BlockTrigger_Config{Interval: interval, ChainId: chainID},
			},
		},
	}
}

func TestValidateTriggerFrequencyCron(t *testing.T) {
	tests := []struct {
		name      string
		trigger   *avsproto.TaskTrigger
		wantError bool
	}{
		// The exact schedule that leaked to prod on 2026-07-17.
		{"minute cron is rejected", cronTrigger("* * * * *"), true},
		{"@every 1s is rejected", cronTrigger("@every 1s"), true},
		{"tight pair is rejected", cronTrigger("0,1 * * * *"), true},
		{"exactly at the floor is allowed", cronTrigger("*/5 * * * *"), false},
		{"6h product default is allowed", cronTrigger("0 */6 * * *"), false},
		// A fast schedule must not be smuggled in behind a slow one.
		{"any schedule in the list is checked", cronTrigger("0 */6 * * *", "* * * * *"), true},
		{"nil trigger is allowed", nil, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTriggerFrequency(tt.trigger, 1)
			if tt.wantError && err == nil {
				t.Error("expected an error, got nil")
			}
			if !tt.wantError && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
		})
	}
}

func TestValidateTriggerFrequencyBlock(t *testing.T) {
	tests := []struct {
		name      string
		trigger   *avsproto.TaskTrigger
		wantError bool
	}{
		// Every block on Ethereum is 7,200 executions/day — five times the
		// cron that caused the incident, and previously only checked for > 0.
		{"every block on Ethereum is rejected", blockTrigger(1, 1), true},
		{"4 blocks on Ethereum is just under the floor", blockTrigger(4, 1), true},
		{"5 blocks on Ethereum clears the floor", blockTrigger(5, 1), false},
		// Base produces blocks 6x faster, so the same interval costs 6x as
		// much — which is why the floor is wall-clock and not a block count.
		{"5 blocks on Base is rejected", blockTrigger(5, 8453), true},
		{"30 blocks on Base clears the floor", blockTrigger(30, 8453), false},
		// An unlisted chain falls back to the strict 1s default.
		{"unknown chain is gated strictly", blockTrigger(30, 999999), true},
		{"unknown chain clears at 60 blocks", blockTrigger(60, 999999), false},
		{"zero interval is rejected", blockTrigger(0, 1), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTriggerFrequency(tt.trigger, 1)
			if tt.wantError && err == nil {
				t.Error("expected an error, got nil")
			}
			if !tt.wantError && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
		})
	}
}

// Event and fixed-time triggers have no statically knowable rate, so the
// frequency floor must not reject them — DefaultMaxExecution is their bound.
func TestValidateTriggerFrequencyIgnoresUnratedTriggers(t *testing.T) {
	fixedTime := &avsproto.TaskTrigger{
		Type: avsproto.TriggerType_TRIGGER_TYPE_FIXED_TIME,
		TriggerType: &avsproto.TaskTrigger_FixedTime{
			FixedTime: &avsproto.FixedTimeTrigger{
				Config: &avsproto.FixedTimeTrigger_Config{Epochs: []int64{1, 2, 3}},
			},
		},
	}
	if err := validateTriggerFrequency(fixedTime, 1); err != nil {
		t.Errorf("fixed-time trigger should not be rate-gated, got %v", err)
	}
}

// Copilot review flagged a nil cron Config as a panic. It is not — generated
// getters are nil-safe, so GetConfig().GetSchedules() returns nil and the loop
// is skipped. The real defect is what that silence means: the trigger cleared
// the frequency floor and would be created, then auto-failed later by the
// ValidateWithError sweep. Both malformed shapes must fail at create instead,
// matching what the block branch already did.
func TestValidateTriggerFrequencyRejectsMalformedCron(t *testing.T) {
	nilConfig := &avsproto.TaskTrigger{
		Type:        avsproto.TriggerType_TRIGGER_TYPE_CRON,
		TriggerType: &avsproto.TaskTrigger_Cron{Cron: &avsproto.CronTrigger{Config: nil}},
	}
	if err := validateTriggerFrequency(nilConfig, 1); err == nil {
		t.Error("a cron trigger with no config must be rejected at create, not left to the sweep")
	}

	if err := validateTriggerFrequency(cronTrigger(), 1); err == nil {
		t.Error("a cron trigger with an empty schedule list must be rejected")
	}
}

// The error must name the chain the block-count boundary was actually computed
// from. An unset chain_id resolves to the aggregator default, so quoting
// "chain 0" alongside a 12s-chain block count sends the caller looking for a
// chain that isn't involved.
func TestBlockFrequencyErrorNamesResolvedChain(t *testing.T) {
	unsetChain := &avsproto.TaskTrigger{
		Type: avsproto.TriggerType_TRIGGER_TYPE_BLOCK,
		TriggerType: &avsproto.TaskTrigger_Block{
			Block: &avsproto.BlockTrigger{
				Config: &avsproto.BlockTrigger_Config{Interval: 1}, // ChainId omitted
			},
		},
	}
	err := validateTriggerFrequency(unsetChain, 1)
	if err == nil {
		t.Fatal("every block on a 12s chain must be rejected")
	}
	if strings.Contains(err.Error(), "chain 0") {
		t.Errorf("error names the unresolved chain 0: %v", err)
	}
	if !strings.Contains(err.Error(), "chain 1") {
		t.Errorf("error should name the resolved fallback chain 1: %v", err)
	}
	// The quoted boundary must match the chain named, not the raw config value.
	if !strings.Contains(err.Error(), "at least 5 blocks") {
		t.Errorf("expected the 12s-chain boundary of 5 blocks: %v", err)
	}
}

// The two floors are deliberately different: reacting to chain state is the
// point of a block trigger, so it gets a tighter bound than cron. This pins
// that they can't silently collapse back into one value.
func TestBlockAndCronFloorsAreDistinct(t *testing.T) {
	if MinBlockTriggerInterval >= MinCronInterval {
		t.Fatalf("block floor %s should be tighter than the cron floor %s",
			MinBlockTriggerInterval, MinCronInterval)
	}

	// 60s: legal as a block trigger on Ethereum (5 x 12s), illegal as cron.
	if err := validateTriggerFrequency(blockTrigger(5, 1), 1); err != nil {
		t.Errorf("5 blocks on Ethereum is exactly the block floor, got %v", err)
	}
	if err := validateTriggerFrequency(cronTrigger("* * * * *"), 1); err == nil {
		t.Error("a 60s cron must still be rejected by the cron floor")
	}
}

// A loop runs its body once per input element, so one execution can spend
// unbounded provider quota no matter how rarely the task fires — the trigger
// floors cannot reach this. The loop caps concurrent workers at 10, but that
// throttles parallelism, not total work.
func TestLoopIterationCap(t *testing.T) {
	addresses := func(n int) []interface{} {
		out := make([]interface{}, n)
		for i := range out {
			out[i] = fmt.Sprintf("0xAddr%d", i)
		}
		return out
	}

	runLoopOver := func(t *testing.T, items []interface{}) *avsproto.Execution_Step {
		t.Helper()
		node, err := CreateNodeFromType("loop", map[string]interface{}{
			"inputVariable":    "{{settings.address_list}}",
			"iterVal":          "value",
			"iterKey":          "index",
			"iterationTimeout": float64(30),
			"executionMode":    "sequential",
			"runner": map[string]interface{}{
				"type": "customCode",
				"config": map[string]interface{}{
					"lang":   avsproto.Lang_LANG_JAVASCRIPT,
					"source": "return value;",
				},
			},
		}, "")
		if err != nil {
			t.Fatalf("CreateNodeFromType: %v", err)
		}
		node.Name = "loopCapTest"

		step, _ := NewVM().RunNodeWithInputs(node, map[string]interface{}{
			"settings": map[string]interface{}{
				"runner":       "0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557",
				"address_list": items,
			},
		})
		return step
	}

	t.Run("at the cap succeeds", func(t *testing.T) {
		step := runLoopOver(t, addresses(MaxLoopIterations))
		if step == nil || !step.Success {
			t.Fatalf("a loop of exactly %d items should run, got %+v", MaxLoopIterations, step)
		}
	})

	t.Run("one over the cap fails loudly", func(t *testing.T) {
		step := runLoopOver(t, addresses(MaxLoopIterations+1))
		if step == nil {
			t.Fatal("expected a step, got nil")
		}
		if step.Success {
			t.Fatal("a loop over the cap must fail, not silently truncate")
		}
		if !strings.Contains(step.Error, "exceeding") {
			t.Errorf("expected the error to name the limit, got: %q", step.Error)
		}
		// The failure must not be a partial run reported as a whole one.
		if out := step.GetLoop(); out != nil && out.Data != nil {
			if arr, ok := out.Data.AsInterface().([]interface{}); ok && len(arr) > 0 {
				t.Errorf("expected no partial results, got %d", len(arr))
			}
		}
	})
}

func TestBlockTimeForChain(t *testing.T) {
	if got := blockTimeForChain(1); got != 12*time.Second {
		t.Errorf("Ethereum block time = %v, want 12s", got)
	}
	if got := blockTimeForChain(42161); got != 250*time.Millisecond {
		t.Errorf("Arbitrum block time = %v, want 250ms (must under-estimate, not use the 1s default)", got)
	}
	if got := blockTimeForChain(56); got != 750*time.Millisecond {
		t.Errorf("BNB block time = %v, want 750ms", got)
	}
	// An unlisted chain must fall back to the *strict* default, never a
	// permissive one — overestimating block time would open a hole.
	if got := blockTimeForChain(424242); got != defaultBlockTime {
		t.Errorf("unknown chain block time = %v, want %v", got, defaultBlockTime)
	}
	if defaultBlockTime > 2*time.Second {
		t.Errorf("defaultBlockTime %v is too permissive to fail safe", defaultBlockTime)
	}
}

func TestCheckActiveWorkflowQuota(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	engine := New(db, testutil.GetAggregatorConfig(), nil, testutil.GetLogger())
	const owner = "0x804e49e8C4eDb560AE7c48B554f6d2e27Bb81557"

	if err := engine.checkActiveWorkflowQuota(owner); err != nil {
		t.Fatalf("an owner with no workflows should pass, got %v", err)
	}

	for i := 0; i < MaxActiveWorkflowsPerOwner-1; i++ {
		id := fmt.Sprintf("task-%d", i)
		engine.tasks[id] = &model.Workflow{
			Task: &avsproto.Task{Id: id, Owner: owner, Status: avsproto.TaskStatus_Enabled},
		}
	}
	if err := engine.checkActiveWorkflowQuota(owner); err != nil {
		t.Fatalf("one slot below the cap should still pass, got %v", err)
	}

	engine.tasks["task-final"] = &model.Workflow{
		Task: &avsproto.Task{Id: "task-final", Owner: owner, Status: avsproto.TaskStatus_Enabled},
	}
	if err := engine.checkActiveWorkflowQuota(owner); err == nil {
		t.Error("expected the cap to reject the next create, got nil")
	}

	// The cap is per owner: a different owner is unaffected by the first
	// owner's tasks, and matching is case-insensitive on the address.
	if err := engine.checkActiveWorkflowQuota("0x0000000000000000000000000000000000000001"); err != nil {
		t.Errorf("a different owner should be unaffected, got %v", err)
	}
	if err := engine.checkActiveWorkflowQuota(strings.ToUpper(owner)); err == nil {
		t.Error("owner matching should be case-insensitive, got nil")
	}
}

// CreateWorkflow must never store a task the executor would treat as uncapped.
// The executor gates on `MaxExecution > 0`, so both 0 and any negative read as
// "run forever" — an `== 0` check would let negatives straight through.
func TestCreateWorkflowDefaultsNonPositiveMaxExecution(t *testing.T) {
	db := testutil.TestMustDB()
	defer storage.Destroy(db.(*storage.BadgerStorage))

	engine := New(db, testutil.GetAggregatorConfig(), nil, testutil.GetLogger())
	user := testutil.TestUser1()

	for _, given := range []int64{0, -1, -DefaultMaxExecution} {
		task := testutil.RestTask()
		task.MaxExecution = given

		created, err := engine.CreateWorkflow(user, task)
		if err != nil {
			t.Fatalf("maxExecution %d: CreateWorkflow failed: %v", given, err)
		}
		if created.MaxExecution != DefaultMaxExecution {
			t.Errorf("maxExecution %d became %d, want the default %d",
				given, created.MaxExecution, DefaultMaxExecution)
		}
		if created.MaxExecution <= 0 {
			t.Errorf("maxExecution %d stayed non-positive (%d) — the executor would treat this task as uncapped",
				given, created.MaxExecution)
		}
	}

	// An explicit positive value is the caller's to choose and must survive.
	task := testutil.RestTask()
	task.MaxExecution = 7
	created, err := engine.CreateWorkflow(user, task)
	if err != nil {
		t.Fatalf("CreateWorkflow with an explicit cap failed: %v", err)
	}
	if created.MaxExecution != 7 {
		t.Errorf("explicit maxExecution 7 became %d", created.MaxExecution)
	}
}
