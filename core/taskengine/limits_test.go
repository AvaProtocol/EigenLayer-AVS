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

func TestBlockTimeForChain(t *testing.T) {
	if got := blockTimeForChain(1); got != 12*time.Second {
		t.Errorf("Ethereum block time = %v, want 12s", got)
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
