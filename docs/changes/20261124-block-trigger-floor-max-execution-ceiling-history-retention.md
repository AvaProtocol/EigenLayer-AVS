# Block Trigger Runtime Floor, Base 0.2s Blocks, maxExecution Ceiling, Execution History Retention

- **Date**: 2026-11-24
- **Status**: Implemented (v6.7.0, deployed 2026-11-25)
- **Branch**: shipped via PRs #935 and #938, released in #937
- **Related**: avs-infra `support/issues/20261109-gateway-volume-and-auth-admission.md` (cleanup step 5: execution-history retention); the 2026-07-28 limits in `core/taskengine/limits.go`

## Problem

The production gateway volume reached 89% on 2026-11-24. The live BadgerDB was 17 GB, and each daily backup was about 13 GB compressed against a 24 GiB backup budget, so the next backup was expected to exceed its budget.

Most of the growth came from one Base block-trigger workflow whose execution records are about 366 KB each. Three limits that should have bounded it did not:

1. **Rate.** The trigger is a block trigger with an interval of 30 on chain 8453, and its creator named it "minute". `chainBlockTime` lists Base at 2s, so 30 blocks came to 60s and passed the 60s `MinBlockTriggerInterval` at create. Base moved to 0.2s blocks on 2026-11-05 (block ~53,556,123); Base Sepolia moved too. From then on the task fired every 6s, 14,400 runs a day. The floor is only checked at create, so nothing noticed.
2. **Lifetime.** `DefaultMaxExecution` (51,840) only applies when `max_execution` is omitted or non-positive. This task was created with `max_execution = 1,000,000,000`, which the server accepts. It had run 150,880 times by 2026-11-24, about 190 years from its cap at 14,400 a day.
3. **Storage.** Execution records (`history:<taskID>:<executionID>`) never expire. At 6s and 366 KB a run, that is about 5.3 GB a day.

## Decision

**Block-trigger floor at runtime** (`limits.go` `admitBlockTrigger`, called from `AggregateChecksResultWithState`). For a task whose stored trigger is a block trigger, a notification that arrives less than `MinBlockTriggerGap` (60s floor minus 6s for delivery jitter) after the last admitted one is dropped. The response is `rate_limited` with `TaskStillEnabled = true` and a positive `RemainingExecutions`, so the operator keeps monitoring. The gate is keyed on the stored trigger, not on `payload.TriggerType`, which the operator sets. A notification arriving while the task has a pending execution (a non-blocking `TriggerWorkflow`, whose operator notification is sent once) is always admitted, and the next scheduled trigger is measured from it. State is in memory; after a restart each task's first trigger is admitted. Throttled tasks log one Info line per admitted run with the number dropped since the previous one.

This holds every block task to about one run a minute, whatever the block-time table says, including tasks created before the floor existed (2026-07-28). Cron is unaffected: its rate comes from the expression, so it cannot drift.

**Base and Base Sepolia at 200ms** in `chainBlockTime`. Measured from public RPC on 2026-11-25: 3,000 blocks in 600s on both. OP Mainnet is still 2s and is unchanged. New Base block tasks now need an interval of at least 300 blocks.

**`MaxExecutionCeiling`** = 525,600 (one year at the block floor). `CreateWorkflow` rejects an explicit `max_execution` above it with `InvalidArgument`, following the retry ceilings: rejected rather than clamped, so the caller learns the bound. There is no update path for `max_execution`. Stored tasks keep their value: applying the ceiling to them would complete running workflows, which is an operational decision, not a code default.

**Execution history retention** (`execution_retention.go`). An hourly sweep (first run 10 minutes after start, every mode) keeps the newest `MaxExecutionHistoryPerTask` = 1,000 records per task and deletes the rest:

- Execution ids are ULIDs, so key order under a task's prefix is creation order. Both passes iterate keys only: pass 1 counts records per task, and pass 2 takes the first `count - 1000` of each over-cap task.
- A record is kept if its execution is still in flight: a `pending:` key, a durable `wake:` subscription, or a `ckpt:` checkpoint. Kept records do not count toward the sweep's delete limit, so they cannot fill a sweep and stall pruning of the tasks after them.
- A pruned run's `trigger:` status key is deleted too; otherwise `GetExecutionStatus` would fall back to it and report the run as PENDING.
- At most 50,000 deletes per sweep, in transactions of 1,000, so a backlog drains over a few hours rather than in one burst.
- Each sweep then runs value-log GC (`Vacuum`, up to 10 rounds). A delete only writes a tombstone. Value-log GC does not reclaim these records on this database; see the correction below.

## Alternatives considered

- **Fix only the block-time table.** It is checked only at create, so existing tasks keep their rate, and it goes stale again the next time a chain changes its block time.
- **Enforce the floor in the operator.** Operators may be on older versions; the aggregator sees every trigger.
- **Clamp `max_execution` instead of rejecting.** The caller would believe a value was stored that was not. Rejecting matches the retry-policy ceilings.
- **Age-based retention (for example 30 days).** It does not bound a fast task: at the block floor 30 days is 43,200 records, about 15 GB at 366 KB each, and a 30-day window would have deleted none of this task's 6s-era records. A count bounds every task to about 400 MB at that record size.
- **A TTL on execution writes.** History is written from several places (final record, durable checkpoint, resume), and a WAITING durable execution must not expire. A TTL would also leave existing records untouched. A sweep covers both.

## Effects to know about

- Block tasks created before 2026-07-28 with intervals under 60s are now throttled to about one run a minute instead of running at their configured rate.
- `GetExecutionCount` counts stored records, so it reports at most 1,000 per task. `task.ExecutionCount` (the run counter) is unchanged.
- Executions older than a task's newest 1,000 return not found from `GetExecution`.
- Execution records are still large (366 KB) for workflows that persist big REST responses. Reducing per-record payload is cleanup step 5's other half and is not addressed here.

## Verification

- Unit tests: `TestAdmitBlockTrigger` (6s dropped, gap boundary, jitter at the floor, per-task, forced admission), `TestAggregateChecksResultDropsEarlyBlockTrigger` (rate_limited keeps the operator monitoring; a pending manual run is admitted inside the gap), `TestCreateWorkflowRejectsMaxExecutionAboveCeiling`, `TestValidateTriggerFrequencyBlock` and `TestBlockTimeForChain` (Base 30 blocks rejected, 300 accepted), `TestPruneExecutionHistory` (cap, in-flight kept, trigger key removed, non-record keys ignored), `TestPruneExecutionHistorySkipsProtectedWithoutSpendingLimit`, `TestPruneExecutionHistoryStopsAtLimit`.
- The dedup tests that send two block notifications in quick succession now backdate the gate, so they still test dedup alone.
- `go run scripts/compare_storage_structure.go origin/staging`: no storage key or model changes.
- After deploy, watch the gateway logs:
  - "Block trigger fires faster than the floor" should appear about once a minute for the 6s task.
  - "Pruned execution history" should report about 50,000 deletes per hour until the backlog drains.
  - The next backup should be much smaller than 13 GB.

## After deploy (2026-11-25)

- v6.7.0 reached the gateway at 08:52 UTC. Badger took about 4 minutes to open its 1,038 tables, and the engine started at 08:56:42.
- The 6s task then ran every ~54s. Each admitted run logged "dropped the early ones" with 8 or 9 dropped. 54s rather than 60s is the gate's 6s jitter allowance at a 6s trigger cadence.
- The first sweep, at 09:07, deleted 50,000 records and hit the per-sweep limit.

## Correction: value-log GC does not reclaim the space

This entry said the sweep's value-log GC would return the disk. It does not on this database. Badger v4's default `ValueThreshold` is 1 MB, so the ~366 KB execution records are stored inline in the LSM tables, not in the value log. On 2026-11-25 the gateway had 1,035 tables totalling 18 GB and a 104 MB value log. Disk for a deleted record returns only when a compaction rewrites the table holding it. The tombstones from a large delete are too small to trigger the compactions that would reach the bottom level, where most of this data sits.

Backups shrink right away regardless, because a backup copies only live keys.

To reclaim the live database, the gateway REPL gained a `flatten` command that runs Badger's `Flatten` with one worker in the background. Run it after the sweep backlog has drained, so that one pass drops all of it.
