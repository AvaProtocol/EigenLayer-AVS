package taskengine

import (
	"fmt"
	"math"
	"strings"
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/robfig/cron/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Constant ceilings on how much work a single task — or a single owner — can
// ever schedule.
//
// Every execution costs money on some metered provider. That is true even for a
// task with no REST node: the worker configs point at metered RPC
// (`eth_rpc_url` → dRPC) and a metered bundler (`alchemy_api_key`), and a task
// with a summarizer enabled spends an LLM call per run. Occurrence *is* the
// cost, so these ceilings are universal rather than conditional on which
// providers a workflow happens to reference. That also sidesteps a detection
// problem: BalanceNode reads `macroSecrets["moralis_api_key"]` directly rather
// than through an `{{apContext.configVars.*}}` template, so no static scan of
// the task body can reliably tell which tasks spend which quota.
//
// These are deliberately compile-time constants, not config: the point is a
// bound that holds in every environment without an operator having to set it,
// and that cannot drift between staging and production.
const (
	// MinCronInterval is the floor on how often a cron-triggered task may
	// fire. At 5 minutes a single task can cost at most 288 executions/day.
	//
	// Motivation: a leaked test workflow on a `* * * * *` cron ran 14,824
	// times over ten days (2026-07-17 → 2026-07-27), spending ~14.8K Moralis
	// and ~14.8K GoPlus calls before anyone noticed. This floor makes that
	// specific shape unconstructible.
	MinCronInterval = 5 * time.Minute

	// MinBlockTriggerInterval is the floor for block triggers, which get a
	// tighter bound than cron because reacting to chain state is the whole
	// point of them — a 5-minute floor would break health-factor and stop-loss
	// alerting outright.
	//
	// 60s (1,440 executions/day) is where responsiveness stops buying anything.
	// A block-triggered workflow that writes on-chain cannot act faster than
	// its UserOp settles, and real bundler round-trips run 120-200s; below a
	// minute the extra polls discover state the task could not have acted on
	// yet. Read-only alerting genuinely wants speed, but a health factor caught
	// 60s later is still caught well before liquidation.
	//
	// Deliberately not tighter than the cadence of the incident above: a floor
	// that still permits the rate you just paid for is hard to justify.
	MinBlockTriggerInterval = time.Minute

	// DefaultMaxExecution is applied when a create request omits
	// `max_execution`. The field was already enforced (executor.go), but an
	// omitted value meant 0, and 0 means unlimited — so the default was
	// infinity.
	//
	// Sized so a workflow running flat out at the cron floor survives six
	// months: 288 executions/day x 180 days. A task that legitimately needs
	// longer can still pass a larger value explicitly; this only has to be
	// generous enough that nobody hits it by accident.
	//
	// Note this is six months only at the *cron* floor. A block-triggered task
	// at its own 60s floor burns 1,440/day and reaches this in ~36 days. One
	// constant cannot give both floors the same wall-clock lifetime while the
	// floors differ 5x, and the shorter bound is the safer place to land.
	DefaultMaxExecution int64 = (24 * 60 / 5) * 180

	// MaxActiveWorkflowsPerOwner bounds the per-account case that per-task
	// ceilings structurally cannot reach: N leaked tasks cost N times the
	// per-task cap. Counts only *active* (Enabled) tasks, so completed and
	// cancelled ones never accumulate against an owner.
	//
	// Sized from a measurement, not a guess. The first value here was 100,
	// chosen with no data; the ava-sdk-js v4 suite then failed 30 of 48 suites
	// against a local gateway with 132 rejections, because a single full run
	// creates ~105 workflows under one EOA (gateway log, 7.7-minute window) and
	// per-test cleanup lags creation enough that the live count crosses 100
	// mid-run. 1000 leaves ~10x headroom over that observed peak.
	//
	// A loose bound is the right trade here. This cap is only a backstop
	// against mass *creation*: DefaultMaxExecution already stops any single
	// leaked task after 51,840 runs and the interval floors bound its rate, so
	// the per-task ceilings do the real work. Against that, being too tight is
	// the more damaging failure — it is the one ceiling evaluated against an
	// owner's *current* state, so an owner already over it is locked out of
	// creating anything, with no grandfathering.
	MaxActiveWorkflowsPerOwner = 1000

	// MaxLoopIterations bounds fan-out *within* a single execution, which the
	// trigger floors cannot reach: a REST node inside a Loop runs once per
	// element of the resolved input array, so one execution can make thousands
	// of provider calls no matter how rarely it fires. The loop already caps
	// concurrent workers at 10, but that throttles parallelism, not total work.
	//
	// Exceeding this is an error rather than a silent truncation — a loop that
	// quietly processed the first 100 of 5,000 addresses would report success
	// while skipping most of its job.
	MaxLoopIterations = 100

	// MaxRetryAttempts bounds a node's RetryPolicy for the same reason
	// MaxLoopIterations bounds a Loop: retry is a client-controlled multiplier
	// on metered provider calls *inside* a single execution, which no trigger
	// floor can reach. Unbounded, `{max_attempts: 100, backoff_ms: 60000}` is
	// 1h39m of sleeping inside one node.
	//
	// 5 attempts is where retrying stops buying anything: with the default 1s
	// backoff and 2x growth that is 15s of waiting, long enough to ride out a
	// provider blip or a rate-limit window, and a failure still standing after
	// four retries is not transient.
	MaxRetryAttempts = 5

	// DefaultRetryBackoff applies when a policy enables retries without setting
	// backoff_ms. Without a default, `{max_attempts: 5}` — the most natural
	// policy to write — retried with *zero* delay, turning one 429 into five
	// immediate hits.
	DefaultRetryBackoff = time.Second

	// MaxRetryBackoff caps any single backoff, whether it came from backoff_ms,
	// max_backoff_ms, or exponential growth. Capping the growing value itself
	// (not a per-attempt copy) is also what keeps the multiplication from
	// overflowing time.Duration.
	MaxRetryBackoff = 30 * time.Second

	// MaxTotalRetryDelay bounds one node's worst-case *total* time asleep, so
	// the three retry knobs cannot be combined into a long stall that each knob
	// individually passes. Checked at create time against the exact schedule the
	// policy produces.
	MaxTotalRetryDelay = 60 * time.Second

	// MaxExecutionRetryDelay bounds retry sleeping across a whole execution.
	// Per-node validation cannot see fan-out: a Loop runs its retryable runner
	// once per element, so MaxLoopIterations nodes each within MaxTotalRetryDelay
	// still multiply to 100 minutes. Enforced at runtime by retryBudget; once
	// spent, later nodes fail on their first attempt instead of extending the
	// execution.
	MaxExecutionRetryDelay = 5 * time.Minute
)

// cronSamples is how many consecutive fire times to inspect when deriving a
// schedule's tightest gap. A schedule's shortest interval need not be its
// first: "0,1 * * * *" fires 60s apart once an hour and ~59m apart otherwise.
// 64 samples covers every sub-daily pattern that can breach the floor.
const cronSamples = 64

// limitsCronParser mirrors the parser spec in trigger/time.go so validation and
// execution agree on what a schedule means. Note `cron.Descriptor` accepts
// "@every 1s" — which is exactly why the check below measures actual fire times
// instead of pattern-matching the expression text.
var limitsCronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// chainBlockTime maps a chain to its block time, used to convert a block
// trigger's block-count interval into wall-clock.
//
// Values here are deliberately *under*-estimates. Underestimating block time
// requires more blocks to clear the floor, which fails safe; overestimating
// would open a hole (assuming 3s blocks on a chain that actually produces them
// every 0.75s would let a task fire 4x more often than the floor intends).
var chainBlockTime = map[int64]time.Duration{
	1:        12 * time.Second,       // Ethereum mainnet
	11155111: 12 * time.Second,       // Sepolia
	8453:     2 * time.Second,        // Base
	84532:    2 * time.Second,        // Base Sepolia
	56:       750 * time.Millisecond, // BNB Smart Chain (post-Maxwell sub-second blocks)
	42161:    250 * time.Millisecond, // Arbitrum One (~250ms; under-estimate vs typical 200–300ms)
	10:       2 * time.Second,        // OP Mainnet (OP-stack, same family as Base)
	130:      200 * time.Millisecond, // Unichain flashblocks ~200ms; 1s sealed blocks. Under-estimate.
	4663:     100 * time.Millisecond, // Robinhood Chain (Arb Orbit; advertised 100ms).
}

// defaultBlockTime is used for a chain absent from the table. Deliberately
// small so an unlisted chain is gated strictly rather than loosely; add the
// chain to the table to relax it.
const defaultBlockTime = time.Second

func blockTimeForChain(chainID int64) time.Duration {
	if blockTime, ok := chainBlockTime[chainID]; ok {
		return blockTime
	}
	return defaultBlockTime
}

// minCronInterval returns the shortest gap between two consecutive fires of the
// given cron expression.
func minCronInterval(expr string) (time.Duration, error) {
	schedule, err := limitsCronParser.Parse(strings.TrimSpace(expr))
	if err != nil {
		return 0, fmt.Errorf("invalid cron schedule %q: %w", expr, err)
	}

	prev := schedule.Next(time.Now().UTC())
	if prev.IsZero() {
		return 0, fmt.Errorf("cron schedule %q never fires", expr)
	}

	shortest := time.Duration(0)
	for i := 0; i < cronSamples; i++ {
		next := schedule.Next(prev)
		// Next returns the zero time when it can't find an activation within
		// its search horizon (e.g. "0 0 30 2 *"). Stop rather than treat the
		// zero value as a gap.
		if next.IsZero() || !next.After(prev) {
			break
		}
		if gap := next.Sub(prev); shortest == 0 || gap < shortest {
			shortest = gap
		}
		prev = next
	}

	if shortest == 0 {
		return 0, fmt.Errorf("cron schedule %q fires at most once", expr)
	}
	return shortest, nil
}

// checkActiveWorkflowQuota rejects a create that would push an owner past
// MaxActiveWorkflowsPerOwner.
//
// Counts n.tasks, which holds only Enabled tasks: it is loaded at boot from the
// Enabled status prefix and entries are deleted when a task completes, fails,
// or is cancelled. Counting the "u:{owner}" storage prefix instead would count
// every task the owner has *ever* created, so dead tasks would accumulate until
// the owner could never create another one.
func (n *Engine) checkActiveWorkflowQuota(owner string) error {
	target := strings.ToLower(owner)

	n.lock.Lock()
	active := 0
	for _, task := range n.tasks {
		if strings.ToLower(task.Owner) == target {
			active++
		}
	}
	n.lock.Unlock()

	if active >= MaxActiveWorkflowsPerOwner {
		return status.Errorf(codes.ResourceExhausted,
			"owner already has %d active workflows, the maximum is %d: cancel an existing workflow before creating another",
			active, MaxActiveWorkflowsPerOwner)
	}
	return nil
}

// validateTriggerFrequency rejects a trigger that would fire faster than its
// own type's floor. Only cron and block triggers have a statically knowable
// rate: fixed-time triggers are bounded by their epoch list, and event triggers
// fire at whatever rate the chain produces matching logs — for those,
// DefaultMaxExecution is the only static bound available.
//
// fallbackChainID supplies the chain for a block trigger that carries none of
// its own. It must be the aggregator's default chain: resolving an unset chain
// to the strict `defaultBlockTime` instead would gate a plain Ethereum block
// trigger as though blocks arrived every second.
func validateTriggerFrequency(trigger *avsproto.TaskTrigger, fallbackChainID int64) error {
	if trigger == nil {
		return nil
	}

	if cronTrigger := trigger.GetCron(); cronTrigger != nil {
		// An absent config would not panic — generated getters are nil-safe, so
		// GetConfig().GetSchedules() yields nil and the loop below is skipped.
		// It would instead pass the floor *silently* and leave the task to be
		// auto-failed later by the ValidateWithError sweep. Reject it here, for
		// the same reason the block branch does: a task that can never fire
		// should fail at create, where the caller can still act on it.
		config := cronTrigger.GetConfig()
		if config == nil {
			return fmt.Errorf("cron trigger config is required but missing")
		}
		if len(config.GetSchedules()) == 0 {
			return fmt.Errorf("cron trigger must have at least one schedule")
		}
		for _, expr := range config.GetSchedules() {
			interval, err := minCronInterval(expr)
			if err != nil {
				return err
			}
			if interval < MinCronInterval {
				return fmt.Errorf(
					"cron schedule %q fires every %s, faster than the %s minimum: every execution spends metered provider quota (RPC, bundler, and any API a node calls), so the floor applies to all workflows",
					expr, interval, MinCronInterval)
			}
		}
	}

	if blockTrigger := trigger.GetBlock(); blockTrigger != nil {
		config := blockTrigger.GetConfig()
		if config == nil {
			return fmt.Errorf("block trigger config is required but missing")
		}
		if config.GetInterval() <= 0 {
			return fmt.Errorf("block trigger interval must be greater than 0, got %d", config.GetInterval())
		}

		// Report the *resolved* chain, not config.GetChainId(). An unset
		// chain_id resolves to the aggregator default, and naming chain 0 while
		// quoting a block count derived from a 12s chain sends the caller
		// looking for a chain that isn't involved.
		chainID := triggerMonitoringChainID(trigger, fallbackChainID)
		blockTime := blockTimeForChain(chainID)
		interval := time.Duration(config.GetInterval()) * blockTime
		if interval < MinBlockTriggerInterval {
			minBlocks := int64(math.Ceil(float64(MinBlockTriggerInterval) / float64(blockTime)))
			return fmt.Errorf(
				"block trigger interval of %d block(s) on chain %d fires every ~%s, faster than the %s minimum: use at least %d blocks",
				config.GetInterval(), chainID, interval, MinBlockTriggerInterval, minBlocks)
		}
	}

	return nil
}

// validateRetryPolicies rejects any node whose retry_policy exceeds the retry
// ceilings, or that sets one where it can never apply.
//
// Following MaxLoopIterations' reasoning, an over-limit policy is an error
// rather than a silent clamp: a task that quietly retried 5 times when it asked
// for 100 would report success while doing something other than what it was
// configured to do, and the caller would never learn the ceiling exists.
func validateRetryPolicies(nodes []*avsproto.TaskNode) error {
	for _, node := range nodes {
		if node == nil {
			continue
		}
		label := node.GetName()
		if label == "" {
			label = node.GetId()
		}
		if err := validateRetryPolicy(label, node.GetRetryPolicy(), nodeSupportsRetry(node)); err != nil {
			return err
		}
	}
	return nil
}

// rejectRetryPolicies is the create / simulate / nodes:run boundary: an
// over-limit policy is InvalidArgument, not a silent clamp. executeWithRetry
// still clamps as defense in depth for tasks stored before this existed.
func rejectRetryPolicies(nodes []*avsproto.TaskNode) error {
	if err := validateRetryPolicies(nodes); err != nil {
		return status.Errorf(codes.InvalidArgument, "%s", err.Error())
	}
	return nil
}

// validateRetryPolicy checks a single node's policy. supported reports whether
// the node's runner is on the retry allowlist.
func validateRetryPolicy(nodeLabel string, policy *avsproto.RetryPolicy, supported bool) error {
	if policy == nil {
		return nil
	}

	// A policy on a node that can never honor it is rejected rather than
	// ignored. The allowlist is enforced structurally at execution time, so an
	// accepted policy on (say) a ContractWrite would do nothing forever, with no
	// error and no warning — the user would believe they had retries.
	if !supported {
		return fmt.Errorf(
			"node %q sets retry_policy, but retries are only supported on idempotent read/off-chain nodes (REST, GraphQL, ContractRead, Balance, and Loops over those): re-invoking an on-chain write could submit the transaction twice",
			nodeLabel)
	}

	if policy.GetMaxAttempts() <= 1 {
		// 0 and 1 both mean "one attempt"; the other fields are inert.
		return nil
	}

	if policy.GetMaxAttempts() > MaxRetryAttempts {
		return fmt.Errorf(
			"node %q sets retry_policy.max_attempts=%d, above the maximum of %d: each attempt spends metered provider quota, so retries are bounded like every other per-execution multiplier",
			nodeLabel, policy.GetMaxAttempts(), MaxRetryAttempts)
	}

	if backoff := time.Duration(policy.GetBackoffMs()) * time.Millisecond; backoff > MaxRetryBackoff {
		return fmt.Errorf(
			"node %q sets retry_policy.backoff_ms=%d (%s), above the %s maximum for a single backoff",
			nodeLabel, policy.GetBackoffMs(), backoff, MaxRetryBackoff)
	}
	if maxBackoff := time.Duration(policy.GetMaxBackoffMs()) * time.Millisecond; maxBackoff > MaxRetryBackoff {
		return fmt.Errorf(
			"node %q sets retry_policy.max_backoff_ms=%d (%s), above the %s maximum for a single backoff",
			nodeLabel, policy.GetMaxBackoffMs(), maxBackoff, MaxRetryBackoff)
	}
	if multiplier := policy.GetBackoffMultiplier(); multiplier < 0 {
		return fmt.Errorf(
			"node %q sets retry_policy.backoff_multiplier=%v: must not be negative (omit it for the default of %v)",
			nodeLabel, multiplier, defaultBackoffMultiplier)
	}

	for _, class := range policy.GetRetryOn() {
		if !isKnownRetryClass(class) {
			return fmt.Errorf(
				"node %q sets an unknown retry_policy.retry_on class %q: valid classes are %s",
				nodeLabel, class, strings.Join(defaultRetryClasses, ", "))
		}
	}

	// The individual knobs can each pass while their combination stalls the
	// execution, so bound the schedule the policy actually produces.
	if total := worstCaseRetryDelay(policy); total > MaxTotalRetryDelay {
		return fmt.Errorf(
			"node %q has a retry_policy that can sleep %s in total (%d attempts starting at %s, x%v), above the %s maximum: lower max_attempts, backoff_ms, or set max_backoff_ms",
			nodeLabel, total, policy.GetMaxAttempts(),
			effectiveInitialBackoff(policy), effectiveMultiplier(policy), MaxTotalRetryDelay)
	}

	return nil
}

func isKnownRetryClass(class string) bool {
	for _, c := range defaultRetryClasses {
		if c == class {
			return true
		}
	}
	return false
}

// effectiveInitialBackoff / effectiveMultiplier resolve the defaults
// executeWithRetry applies, so validation measures the same schedule that will
// actually run.
func effectiveInitialBackoff(policy *avsproto.RetryPolicy) time.Duration {
	backoff := time.Duration(policy.GetBackoffMs()) * time.Millisecond
	if backoff <= 0 {
		backoff = DefaultRetryBackoff
	}
	if cap := effectiveMaxBackoff(policy); backoff > cap {
		backoff = cap
	}
	return backoff
}

func effectiveMultiplier(policy *avsproto.RetryPolicy) float64 {
	multiplier := policy.GetBackoffMultiplier()
	if multiplier <= 0 {
		multiplier = defaultBackoffMultiplier
	}
	return multiplier
}

func effectiveMaxBackoff(policy *avsproto.RetryPolicy) time.Duration {
	maxBackoff := time.Duration(policy.GetMaxBackoffMs()) * time.Millisecond
	if maxBackoff <= 0 || maxBackoff > MaxRetryBackoff {
		maxBackoff = MaxRetryBackoff
	}
	return maxBackoff
}

// worstCaseRetryDelay sums every backoff a fully-exhausted policy would sleep:
// max_attempts-1 gaps, growing by the multiplier and clamped by the per-backoff
// cap. This is the worst case by construction — an attempt that succeeds, or
// fails with a non-retryable error, sleeps less.
func worstCaseRetryDelay(policy *avsproto.RetryPolicy) time.Duration {
	attempts := int(policy.GetMaxAttempts())
	if attempts > MaxRetryAttempts {
		attempts = MaxRetryAttempts
	}
	backoff := effectiveInitialBackoff(policy)
	multiplier := effectiveMultiplier(policy)
	maxBackoff := effectiveMaxBackoff(policy)

	total := time.Duration(0)
	for i := 0; i < attempts-1; i++ {
		total += backoff
		backoff = nextBackoff(backoff, multiplier, maxBackoff)
	}
	return total
}
