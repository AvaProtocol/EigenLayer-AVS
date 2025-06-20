package trigger

import (
	"context"
	"fmt"
	"sync"
	"time"

	"math/big"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
)

type TriggerMetadata[T any] struct {
	TaskID string

	Marker T
}

type BlockTrigger struct {
	*CommonTrigger

	// New unified registry (preferred)
	registry *TaskRegistry

	// Legacy schedule map (for backward compatibility)
	schedule   map[int64]map[string]bool
	legacyMode bool

	// Smart monitoring state
	minInterval        int64                 // Minimum interval across all tasks
	subscription       ethereum.Subscription // Active block subscription
	subscriptionActive bool                  // Whether we're currently subscribed
	mu                 sync.RWMutex          // Use RWMutex for better concurrency

	// channel that we will push the trigger information back
	triggerCh chan TriggerMetadata[int64]
}

func NewBlockTrigger(o *RpcOption, triggerCh chan TriggerMetadata[int64], logger sdklogging.Logger) *BlockTrigger {
	var err error

	//logger, err := sdklogging.NewZapLogger(sdklogging.Production)
	b := BlockTrigger{
		CommonTrigger: &CommonTrigger{
			done:      make(chan bool),
			shutdown:  false,
			rpcOption: o,

			logger: logger,
			mu:     sync.Mutex{},
		},
		registry:           NewTaskRegistry(),
		schedule:           make(map[int64]map[string]bool),
		legacyMode:         false, // Start in new mode
		minInterval:        0,
		subscriptionActive: false,
		triggerCh:          triggerCh,
	}

	b.ethClient, err = ethclient.Dial(o.RpcURL)
	if err != nil {
		panic(err)
	}

	b.wsEthClient, err = ethclient.Dial(o.WsRpcURL)

	if err != nil {
		panic(err)
	}

	return &b
}

// ensureNewFormat ensures we're using the new TaskRegistry format
// This provides automatic conversion from legacy schedule map format
func (b *BlockTrigger) ensureNewFormat() {
	if b.legacyMode {
		b.logger.Info("🔄 Converting from legacy schedule map format to new TaskRegistry format")

		// Convert existing data
		b.convertFromScheduleMap()

		// Clear old data
		b.schedule = make(map[int64]map[string]bool)
		b.legacyMode = false

		b.logger.Info("✅ Successfully converted to new TaskRegistry format",
			"task_count", b.registry.GetBlockTaskCount())
	}
}

// ensureLegacyConversion consolidates legacy data detection and conversion
// This helper function eliminates code duplication across methods
func (b *BlockTrigger) ensureLegacyConversion() {
	b.detectLegacyData()
	b.ensureNewFormat()
}

// detectLegacyData checks if we have data in the old format
func (b *BlockTrigger) detectLegacyData() {
	hasLegacyData := len(b.schedule) > 0

	if hasLegacyData && b.registry.GetBlockTaskCount() == 0 {
		b.legacyMode = true
		b.logger.Info("🔍 Detected legacy schedule map data - will convert on next operation")
	}
}

// convertFromScheduleMap converts old schedule map data to the new TaskRegistry
// This provides backward compatibility during migration
func (b *BlockTrigger) convertFromScheduleMap() {
	for interval, tasks := range b.schedule {
		for taskID := range tasks {
			// Create BlockTaskData for the new registry
			blockData := &BlockTaskData{
				Interval: interval,
			}

			// We don't have the original TaskMetadata in the old format,
			// so we'll create a minimal one for compatibility
			taskMetadata := &avsproto.SyncMessagesResp_TaskMetadata{
				TaskId: taskID,
			}

			// Add to new registry
			b.registry.AddTask(taskID, taskMetadata, nil, blockData, nil)

			b.logger.Debug("🔄 Converted legacy block task",
				"task_id", taskID,
				"interval", interval)
		}
	}
}

// calculateMinInterval finds the minimum interval among all registered tasks
func (b *BlockTrigger) calculateMinInterval() int64 {
	// Auto-convert from legacy format if needed
	b.ensureLegacyConversion()

	minInterval := int64(0)

	b.registry.RangeBlockTasks(func(taskID string, entry *TaskEntry) bool {
		interval := entry.BlockData.Interval
		if minInterval == 0 || interval < minInterval {
			minInterval = interval
		}
		return true // Continue iteration
	})

	return minInterval
}

// shouldCheckAtBlock determines if we should perform checks at the given block number
// Using checkpoint system starting from block 0: check at minInterval, 2*minInterval, 3*minInterval, etc.
func (b *BlockTrigger) shouldCheckAtBlock(blockNumber int64) bool {
	if b.minInterval <= 0 {
		return false
	}

	return blockNumber%b.minInterval == 0
}

func (b *BlockTrigger) AddCheck(check *avsproto.SyncMessagesResp_TaskMetadata) error {
	// Auto-convert from legacy format if needed
	b.ensureLegacyConversion()

	b.mu.Lock()
	defer b.mu.Unlock()

	interval := check.GetTrigger().GetBlock().GetConfig().GetInterval()

	// Validate interval to prevent division by zero
	if interval <= 0 {
		return fmt.Errorf("invalid block trigger interval %d for task %s: interval must be greater than 0", interval, check.TaskId)
	}

	// Create BlockTaskData for the new registry
	blockData := &BlockTaskData{
		Interval: interval,
	}

	// Add to new registry
	b.registry.AddTask(check.TaskId, check, nil, blockData, nil)

	// Count tasks with the same interval for logging
	tasksWithSameInterval := 0
	b.registry.RangeBlockTasks(func(taskID string, entry *TaskEntry) bool {
		if entry.BlockData.Interval == interval {
			tasksWithSameInterval++
		}
		return true
	})

	if tasksWithSameInterval == 1 {
		b.logger.Info("📦 First task registered for block interval", "interval", interval, "task_id", check.TaskId)
	} else {
		b.logger.Debug("📦 Additional task registered for block interval", "interval", interval, "task_id", check.TaskId, "total_tasks_for_interval", tasksWithSameInterval)
	}

	// Recalculate minimum interval
	newMinInterval := b.calculateMinInterval()

	if newMinInterval != b.minInterval {
		oldMinInterval := b.minInterval
		b.minInterval = newMinInterval

		// Count unique intervals for logging
		uniqueIntervals := make(map[int64]bool)
		b.registry.RangeBlockTasks(func(taskID string, entry *TaskEntry) bool {
			uniqueIntervals[entry.BlockData.Interval] = true
			return true
		})

		b.logger.Info("🔄 Block monitoring minimum interval updated",
			"old_min_interval", oldMinInterval,
			"new_min_interval", newMinInterval,
			"total_intervals", len(uniqueIntervals))

		// Log active intervals for debugging
		activeIntervals := make([]int64, 0, len(uniqueIntervals))
		for interval := range uniqueIntervals {
			activeIntervals = append(activeIntervals, interval)
		}
		b.logger.Debug("📊 Active block intervals", "intervals", activeIntervals, "min_interval", newMinInterval)
	}

	return nil
}

func (b *BlockTrigger) RemoveCheck(taskID string) error {
	// Auto-convert from legacy format if needed
	b.ensureLegacyConversion()

	b.mu.Lock()
	defer b.mu.Unlock()

	removedFromInterval := int64(0)

	// Get the task before removing it to know which interval it was from
	if entry, exists := b.registry.GetTask(taskID); exists && entry.BlockData != nil {
		removedFromInterval = entry.BlockData.Interval

		// Count remaining tasks with the same interval before removal
		remainingTasksForInterval := 0
		b.registry.RangeBlockTasks(func(tID string, e *TaskEntry) bool {
			if e.BlockData.Interval == removedFromInterval && tID != taskID {
				remainingTasksForInterval++
			}
			return true
		})

		// Remove from new registry
		removed := b.registry.RemoveTask(taskID)

		if removed {
			b.logger.Debug("🗑️ Removed block task", "task_id", taskID, "interval", removedFromInterval, "remaining_tasks_for_interval", remainingTasksForInterval)

			// Check if this was the last task for this interval
			if remainingTasksForInterval == 0 {
				b.logger.Info("🧹 Cleaned up empty block interval", "interval", removedFromInterval)
			}
		} else {
			b.logger.Debug("🤷 Task not found for removal", "task_id", taskID)
		}
	}

	// Recalculate minimum interval after removal
	newMinInterval := b.calculateMinInterval()

	if newMinInterval != b.minInterval {
		oldMinInterval := b.minInterval
		b.minInterval = newMinInterval

		// Count remaining intervals
		uniqueIntervals := make(map[int64]bool)
		b.registry.RangeBlockTasks(func(taskID string, entry *TaskEntry) bool {
			uniqueIntervals[entry.BlockData.Interval] = true
			return true
		})

		b.logger.Info("🔄 Block monitoring minimum interval updated after removal",
			"removed_from_interval", removedFromInterval,
			"old_min_interval", oldMinInterval,
			"new_min_interval", newMinInterval,
			"remaining_intervals", len(uniqueIntervals))

		// If no more tasks, we'll stop subscription in the main loop
		if newMinInterval == 0 {
			b.logger.Info("⏹️ No more block monitoring tasks - will stop subscription")
		}
	}

	return nil
}

func (b *BlockTrigger) Run(ctx context.Context) error {
	b.logger.Debug("🚀 Starting smart block trigger monitor")

	go func() {
		var headers chan *types.Header

		for {
			select {
			case <-ctx.Done():
				b.logger.Info("🛑 Block trigger shutting down due to context cancellation")
				return
			case <-b.done:
				b.logger.Info("🛑 Block trigger shutting down due to done signal")
				return
			default:
				// Check if we need to manage subscription
				b.mu.RLock()
				needsSubscription := b.minInterval > 0
				currentlySubscribed := b.subscriptionActive
				b.mu.RUnlock()

				if needsSubscription && !currentlySubscribed {
					// Start subscription
					b.logger.Info("🔄 Starting block subscription", "min_interval", b.minInterval)
					headers = make(chan *types.Header)
					sub, err := b.wsEthClient.SubscribeNewHead(ctx, headers)
					if err != nil {
						b.logger.Error("❌ Failed to start block subscription", "error", err)
						continue
					}

					b.mu.Lock()
					b.subscription = sub
					b.subscriptionActive = true
					b.mu.Unlock()

					b.logger.Info("✅ Block subscription started", "rpc", b.rpcOption.WsRpcURL, "min_interval", b.minInterval)

				} else if !needsSubscription && currentlySubscribed {
					// Stop subscription
					b.logger.Info("⏹️ Stopping block subscription - no tasks remaining")

					b.mu.Lock()
					if b.subscription != nil {
						b.subscription.Unsubscribe()
						b.subscription = nil
					}
					b.subscriptionActive = false
					b.mu.Unlock()

					if headers != nil {
						close(headers)
						headers = nil
					}

					b.logger.Info("✅ Block subscription stopped")
				}

				// Process blocks if subscribed
				if currentlySubscribed && headers != nil {
					select {
					case err := <-b.subscription.Err():
						if err != nil {
							b.logger.Error("🔄 Block subscription error, retrying", "error", err)

							b.mu.Lock()
							if b.subscription != nil {
								b.subscription.Unsubscribe()
							}
							b.subscriptionActive = false
							b.mu.Unlock()

							if b.wsEthClient != nil {
								b.wsEthClient.Close()
							}

							if err := b.retryConnectToRpc(); err != nil {
								b.logger.Error("❌ Failed to reconnect to RPC", "error", err)
							}
						}

					case header := <-headers:
						if header != nil {
							b.processBlock(header)
						}

					default:
						// Non-blocking check - add small sleep to prevent busy-waiting
						time.Sleep(10 * time.Millisecond)
					}
				}

				// Add a small delay to prevent busy-waiting when no action is needed
				time.Sleep(10 * time.Millisecond)
			}
		}
	}()

	return nil
}

func (b *BlockTrigger) processBlock(header *types.Header) {
	blockNumber := header.Number.Int64()
	b.progress = blockNumber

	b.mu.RLock()
	shouldCheck := b.shouldCheckAtBlock(blockNumber)
	minInterval := b.minInterval
	b.mu.RUnlock()

	if !shouldCheck {
		// Skip processing - not a checkpoint block
		return
	}

	b.logger.Debug("📦 Processing checkpoint block",
		"block_number", blockNumber,
		"block_hash", header.Hash().Hex(),
		"min_interval", minInterval)

	// Create a snapshot of tasks grouped by interval for processing
	intervalTasksMap := make(map[int64][]string)

	b.registry.RangeBlockTasks(func(taskID string, entry *TaskEntry) bool {
		interval := entry.BlockData.Interval
		intervalTasksMap[interval] = append(intervalTasksMap[interval], taskID)
		return true
	})

	triggeredTasks := 0

	// Pre-allocate reusable big.Int instances to avoid allocations in the loop
	intervalBig := new(big.Int)
	remainder := new(big.Int)

	// Check all intervals to see which ones should trigger at this block
	for interval, tasks := range intervalTasksMap {
		if len(tasks) == 0 {
			continue
		}

		// Check if this interval should trigger at this block number
		// Using checkpoint system: interval should trigger when blockNumber % interval == 0
		intervalBig.SetInt64(interval)
		if remainder.Mod(header.Number, intervalBig).Cmp(big.NewInt(0)) == 0 {
			for _, taskID := range tasks {
				b.triggerCh <- TriggerMetadata[int64]{
					TaskID: taskID,
					Marker: blockNumber,
				}
				triggeredTasks++
			}

			b.logger.Debug("🎯 Triggered tasks for interval",
				"interval", interval,
				"block_number", blockNumber,
				"tasks_triggered", len(tasks))
		}
	}

	if triggeredTasks > 0 {
		b.logger.Info("✅ Block checkpoint processed",
			"block_number", blockNumber,
			"tasks_triggered", triggeredTasks,
			"min_interval", minInterval)
	}
}
