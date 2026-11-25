package taskengine

// Execution history retention.
//
// Every run writes history:<taskID>:<executionID> and nothing deleted it, so
// history grew with run count for as long as a task ran. In 2026-11 one Base
// block task firing every 6s, with ~366 KB records, was most of the gateway's
// 17 GB database and pushed its volume to 89%.
//
// The sweep keeps each task's newest MaxExecutionHistoryPerTask records and
// deletes the rest. Execution ids are ULIDs, so under one task's prefix key
// order is creation order: finding the oldest needs no value reads, and both
// passes below iterate keys only.

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

const (
	executionRetentionFirstRun = 10 * time.Minute
	executionRetentionInterval = time.Hour

	// executionRetentionMaxDeletes bounds one sweep, so a backlog drains over
	// several hourly sweeps rather than in one burst of writes on a live
	// gateway. 50,000 an hour is far above what any task can add at the
	// trigger floors.
	executionRetentionMaxDeletes = 50_000
	executionRetentionBatch      = 1000

	// executionRetentionVacuumRounds bounds value-log GC per sweep. A delete
	// only writes a tombstone; the record bodies stay in Badger's value log
	// until GC rewrites the file they sit in, one file per round.
	executionRetentionVacuumRounds = 10
)

var errRetentionSweepFull = errors.New("retention sweep reached its delete limit")

// batchDeleter is BadgerStorage's AtomicWrite, which Storage does not expose:
// one transaction per batch instead of one per key.
type batchDeleter interface {
	AtomicWrite(map[string][]byte, [][]byte) error
}

func (n *Engine) runExecutionRetentionLoop(ctx context.Context) {
	timer := time.NewTimer(executionRetentionFirstRun)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			n.sweepExecutionHistory(ctx)
			timer.Reset(executionRetentionInterval)
		}
	}
}

func (n *Engine) sweepExecutionHistory(ctx context.Context) {
	deleted, err := pruneExecutionHistory(ctx, n.db, MaxExecutionHistoryPerTask, executionRetentionMaxDeletes)
	if err != nil {
		n.logger.Error("Execution history sweep failed", "deleted", deleted, "error", err)
	} else if deleted > 0 {
		n.logger.Info("Pruned execution history",
			"deleted", deleted,
			"keep_per_task", MaxExecutionHistoryPerTask,
			"hit_sweep_limit", deleted >= executionRetentionMaxDeletes)
	}

	// Every sweep, not only one that deleted: GC can only reclaim a file once
	// compaction has recorded its discards, which lags the deletes.
	for i := 0; i < executionRetentionVacuumRounds && ctx.Err() == nil; i++ {
		if err := n.db.Vacuum(); err != nil {
			break // badger.ErrNoRewrite: nothing left worth rewriting
		}
	}
}

// pruneExecutionHistory deletes the oldest execution records of every task
// holding more than keep, up to maxDeletes in this call, and returns how many
// it deleted. A record whose execution is still in flight (queued, or a
// durable execution waiting on a wake) is kept.
func pruneExecutionHistory(ctx context.Context, db storage.Storage, keep, maxDeletes int) (int, error) {
	prefix := []byte("history:")

	// Pass 1: records per task. Keyed by the key minus its execution id, so a
	// legacy chain-scoped history:{chainID}:{taskID}: run is its own
	// contiguous group.
	counts := make(map[string]int)
	err := db.IterateKeysOnly(prefix, func(key []byte) error {
		if group, ok := executionHistoryGroup(key); ok {
			counts[string(group)]++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	// Pass 2: the first count-keep keys of each over-cap group are its oldest.
	// Records written since pass 1 sort after them, so they are never taken.
	var candidates [][]byte
	taken := make(map[string]int)
	err = db.IterateKeysOnly(prefix, func(key []byte) error {
		if len(candidates) >= maxDeletes {
			return errRetentionSweepFull
		}
		group, ok := executionHistoryGroup(key)
		if !ok {
			return nil
		}
		g := string(group)
		if taken[g] >= counts[g]-keep {
			return nil
		}
		taken[g]++
		candidates = append(candidates, append([]byte(nil), key...))
		return nil
	})
	if err != nil && !errors.Is(err, errRetentionSweepFull) {
		return 0, err
	}

	var doomed [][]byte
	for _, key := range candidates {
		parsed, perr := ParseExecutionKey(key)
		if perr != nil && !errors.Is(perr, ErrLegacyKey) {
			continue
		}
		stub := &model.Workflow{Task: &avsproto.Task{Id: parsed.TaskID}}
		if existsKey(db, PendingExecutionKey(stub, parsed.ExecutionID)) ||
			existsKey(db, wakeSubscriptionKey(parsed.ExecutionID)) ||
			existsKey(db, checkpointKey(parsed.ExecutionID)) {
			continue
		}
		doomed = append(doomed, key)
		// A run that failed leaves its PENDING status key behind, and
		// GetExecutionStatus falls back to it once the record is gone.
		if trigger := TaskTriggerKey(stub, parsed.ExecutionID); existsKey(db, trigger) {
			doomed = append(doomed, trigger)
		}
	}

	deleted := 0
	for start := 0; start < len(doomed); start += executionRetentionBatch {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		end := min(start+executionRetentionBatch, len(doomed))
		if err := deleteKeys(db, doomed[start:end]); err != nil {
			return deleted, err
		}
		for _, key := range doomed[start:end] {
			if bytes.HasPrefix(key, prefix) {
				deleted++
			}
		}
	}
	return deleted, nil
}

// executionHistoryGroup returns a history key without its execution id, or
// false for a key that is not an execution record.
func executionHistoryGroup(key []byte) ([]byte, bool) {
	cut := bytes.LastIndexByte(key, ':')
	// Execution ids are ULIDs, the same length as task ids.
	if cut < 0 || len(key)-cut-1 != TaskIDLength {
		return nil, false
	}
	return key[:cut+1], true
}

func existsKey(db storage.Storage, key []byte) bool {
	exists, _ := db.Exist(key)
	return exists
}

func deleteKeys(db storage.Storage, keys [][]byte) error {
	if w, ok := db.(batchDeleter); ok {
		return w.AtomicWrite(nil, keys)
	}
	for _, key := range keys {
		if err := db.Delete(key); err != nil {
			return err
		}
	}
	return nil
}
